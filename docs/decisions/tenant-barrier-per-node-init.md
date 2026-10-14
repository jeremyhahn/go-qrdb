# Per-node barrier keys: every tenant on its own keys

**Status (2026-10-08): decided.**

- **Decided:** the owner rulings listed under [Decided](#decided). Where a
  section below still reads otherwise, the ruling wins, and the section says
  so.
- **Still open:** none. Question 6 (the tenant-signed serving set) was decided
  on 2026-10-08, and moving off `none` in place (SL-02) is folded in as PN-8.

**Implemented:** PN-2 — the init statement, its verification, its guarded
claim and compare-and-set, the watcher and the `per-node-barrier-keys` gate
([../barrier-seal/architecture.md](../barrier-seal/architecture.md#per-node-barrier-keys)).
*(Corrected 2026-10-14: PN-2 had advertised the feature and wired the gate
before any node composed the book or the watcher; both were withdrawn until
that composition shipped. Every node, standalone and clustered, now composes
the watcher, onboarding pins the epoch-1 anchor, every client transport signs
the statement, and the tenant service claims it through the book; the feature
is advertised again and gates isolated tenants and the claim. Key delivery
(`DistributeTenantKey`) still runs beside the watcher until it is deleted (§14);
a node keeps whichever key reaches it first. The
"Implemented" notes below for PN-3 to PN-8 describe work that has not landed
in this tree; the current status is in
[../barrier-seal/architecture.md](../barrier-seal/architecture.md#per-node-barrier-keys).)* The notes in
[../multi-tenancy/architecture.md](../multi-tenancy/architecture.md) and
[../multi-tenancy/engineering.md](../multi-tenancy/engineering.md) point back
here.

**Implemented:** the tenant-signed serving set (open question 6, option a) —
the signed record, the placement gate, the replica-count bound, and
submit/read on every transport, the CLI and the TypeScript SDK
([../barrier-seal/architecture.md](../barrier-seal/architecture.md#per-node-barrier-keys),
[../multi-tenancy/cli.md](../multi-tenancy/cli.md#qrdb-tenant-barrier-serving-set)).

**Implemented:** PN-6's bootstrap records and passphrase verifier — the
verifier salt in a passphrase statement, the barrier-wide verifier, the
per-node bootstrap record checked at unseal, and a joiner's check of its
passphrase against a serving peer's verifier through the
`tenant.PassphraseVerifierSource` seam, and the `clusterkeys` HPKE verifier
mode behind it (`POST /v1/cluster/verifier`, admitting only a joiner the
signed serving set names)
([../barrier-seal/architecture.md](../barrier-seal/architecture.md#passphrase-verifier-and-bootstrap-record)).

**Implemented:** PN-6's joiner init for every credential kind, each gated on
the tenant-signed serving set through the `tenant.BarrierInitAdmission` seam
before any key exists: `none` and KMS/hardware keys self-initialize through
`BarrierKeyProvider` with the statement's `KeyRef` (the watcher retries a key
the provider cannot yet supply), passphrase keys check the HPKE-delivered
verifier, and a Shamir joiner is armed by the watcher with the statement's
roster and `UnsealKeyCheck` (`TenantService.TenantBarrierAwaitShamirQuorum`)
and generates its own key at the first quorum of PN-5's holder share fan-out
([../barrier-seal/architecture.md](../barrier-seal/architecture.md#joining-node-init-and-admission)).
The scale-up suite proves it on four nodes: a KMS joiner refuses to serve
until its KMS grants it the owner's key and then serves, and a passphrase
joiner refuses a wrong passphrase and accepts the right one.

**Implemented:** PN-6's §14 deletions and the platform's statement. The
tenant-key transport (`clusterkeys` key modes, the `pkg/seal` root-key export
and import) is gone; only the passphrase verifier crosses between nodes. A
`raft` tenant that chose auto-unseal (`CustodyPlatform`) is created with an
epoch-1 statement the creating node signs with its own key under its cluster
certificate, claimed once the tenant record commits (a refused claim undoes
the create). It verifies only against the cluster CA, and only for a tenant
whose record chose platform custody. Each other node it is placed on
initializes its own key under its own node barrier when it sees the
statement; no owner call is needed
([../barrier-seal/architecture.md](../barrier-seal/architecture.md#joining-node-init-and-admission)).

**Implemented:** PN-5 — Shamir per node: the unseal key check the split
returns with its roster, the statement the owner signs over both and claims on
the splitting node, the watcher arming every other serving node without a key,
the holder share fan-out (`qrdb tenant barrier-quorum submit --node`), and the
root key each node generates at its holders' first quorum once the
reconstructed unseal key matches the check
([../barrier-seal/architecture.md](../barrier-seal/architecture.md#how-a-node-takes-part-tenant-barriers),
[../barrier-seal/engineering.md](../barrier-seal/engineering.md#shamir-tenant-barriers)).

**Implemented:** PN-7 — unseal and rekey fan-out: `--all` on `qrdb tenant
barrier-unseal`, `barrier-change-passphrase` and `barrier-quorum submit`; the
passphrase change as the barrier's next credential epoch, signed once and
claimed by the first node; the stale-credential fence
(`StaleCredentialEpochError`) and the old-and-new catch-up that re-wraps a
returning node's key
([../barrier-seal/architecture.md](../barrier-seal/architecture.md#credential-epochs-and-the-fan-out),
[../barrier-seal/engineering.md](../barrier-seal/engineering.md#credential-epochs)).

**Implemented:** PN-8 — enable-encryption per node, SL-02 folded in. A tenant
barrier moves off `none` by its owner's next signed `InitStatement` epoch
(`qrdb tenant barrier-sign-strategy-change`, then `barrier enable-encryption
--init-statement`), the same for every tenant, strict included. The accepting
node requires a complete backup that verifies (`--backup-id`;
`MigrationBackupUnverifiedError` otherwise), records the epoch, holds the
passphrase in memory for 15 minutes and moves its own key; every other serving
node fetches the passphrase over HPKE at `POST /v1/cluster/passphrase`
(`strategy-passphrase` envelope) and re-encrypts under a key of its own.
Tenant barrier status reports the node's `key_fingerprint`. A passphrase
change (PN-7) is the same signed change, to passphrase on a passphrase barrier
(`SignTenantStrategyChange`, claimed by `AdvanceTenantStrategy`): one epoch
model, reported as `credential_epoch`.
The node barrier's cluster migration fetches its passphrase the same way
(`node-passphrase`, admitted by the record's owners). The `clustermig` tenant
arm, `seal.migration_credentials_dir` with its files, and
`StrictEncryptionMigrationError` are deleted outright
([../barrier-seal/architecture.md](../barrier-seal/architecture.md#moving-a-barrier-off-none)).

**Terminology.** A **platform-sealed** tenant key is one wrapped under a key
the platform, not the tenant, controls — for example under a node's barrier.
The code names this mode `SystemManaged` (`deliverSystemManaged`,
`AllowSystemManagedBarrier`). This page says *platform-sealed* everywhere, so
that it is never confused with the **system tenant**, which is the platform's
own tenant.

## Decided

| Ruling (2026-10-08) | What it fixes |
|---|---|
| **Option A approved.** | Init is node-local where no client credential is needed, and fanned out by the client where one is. Both are driven by a signed, keyless init statement. |
| **Passphrase joiner verifier: HPKE from a serving peer (open question 1, option a, of the first draft).** | An unsealed serving node delivers the tenant-wide verifier to a joining node over the `clusterkeys` HPKE channel. |
| **KMS `KeyRef`: one tenant-controlled key by default, a per-node template allowed (open question 2, option a).** | The statement's `KeyRef` names one KMS key the tenant controls, and the tenant grants each node's identity. A provider may resolve `KeyRef` as a per-node template instead. |
| **One barrier mechanism (open question 1, option a).** | `BarrierKeyProvider` covers the node barrier, isolated-tenant barriers and database barriers; the system barrier is folded in (PN-12). |
| **Auto-unseal is the tenant's choice (open question 2: neither option).** | There is **no** strict-mode refusal of platform-sealed keys and no `PlatformSealedKeyError`. `Custody` records the tenant's opt-in, and the auditable barrier status shows it. qrdb ships no native hardware or cloud crypto, only seams; `KeyRef` stays opaque. |
| **Shared tenants: the envelope scheme is rejected (open question 3).** | The shared Raft group is one more tenant with its own barrier, platform-managed. Tenants in it have no keys of their own, and **their data is readable by the platform**. A tenant that needs confidentiality from the platform chooses an isolated type. PN-11 is withdrawn. |
| **Serving set = the tenant's replica count, a tenant quota (open question 4, replaced).** | The system owner or a system admin sets it through the existing quota and entitlement records; a tenant admin cannot change its own. It replaces the PN-13 cap. |
| **MT-17 is not a prerequisite (open question 5, option a).** | MT-18 depends on MT-15 and the cluster seal record only. |
| **The tenant signs its serving set (open question 6, option a).** | The tenant signs the set of nodes that may hold its key with the same signer chain as its `InitStatement`; placement adds members only for nodes in that set. For a tenant that chose auto-unseal, the platform signs. Part of PN-6. |
| **Moving off `none` in place is a signed epoch (SL-02, folded in).** | `none` → keyed is a new signed `InitStatement` epoch through `BarrierKeyProvider`, the same for every tenant type, strict included. The passphrase reaches other nodes over HPKE from a serving peer. `seal.migration_credentials_dir` is deleted. A verified backup is a precondition only; there is no separate backup/restore migration path. Part of PN-8. |

## The governing requirement

The owner's requirement, which this revision is written to meet:

- **Every tenant has its own keys.** That means the system tenant, every
  shared tenant and every isolated tenant.
- **The system owner cannot view or access any tenant's data.**
- **No tenant can access another tenant's data.**
- **Each tenant manages its own keys.**
- **Every tenant works the same way.**

**As ruled (2026-10-08):** the requirement holds for isolated tenants. A
shared tenant has no keys of its own and its data is readable by the platform
(§8); a tenant that needs confidentiality from the platform chooses an
isolated type.

[Limits](#limits) states plainly what this design does not reach.

## The problem

On a cluster of more than one node, an isolated tenant (`raft`, `barrier` or
`volume`) whose key is sealed by its own credential cannot finish onboarding:

1. **Tenant create places the home group on every node.** The home group gets
   a replication factor of `max(ClusterSize(), 1)` and `DedicatedWAL = true`
   (`pkg/service/tenant_home.go`, `ProvisionTenantHome`).
2. **`barrier init` runs on one node only.** That node resolves a strategy,
   registers the tenant barrier and initializes it (`pkg/tenant/service.go`,
   `resolveStrategy`, `InitializeTenantWithStrategy`). No other node has a
   tenant barrier.
3. **The home group never reaches quorum.** Every other node defers its
   home-group replica, because the barrier is not registered there or is
   sealed (`pkg/server/tenant_shard_reconciler.go`, `ensureStarted`). With one
   replica started out of three, the group has no quorum, the pending first
   admin is never written, and init never completes.

**Key transport was the only path.** Before this design the only way a second
node got the tenant barrier was `clusterkeys.Sender.DistributeTenantKey`, and
both of its modes gave every replica the same key (both deleted in PN-6; see
§14):

- **Platform-sealed:** it exports the root key (`deliverSystemManaged` →
  `ExportRootKeyContext`).
- **Tenant-sealed:** it ships the sealed blob (`deliverSelfManaged` →
  `ExportSealedBlob`).

Both modes are forbidden by the cluster-wide sealing ruling.

**What the test suite shows:**

- `TestClusterEnableEncryption_TenantScope_RF3` fails on its expected follower
  key delivery.
- The replica-forwarding and mixed-cluster suites fail with `tenant: barrier is
  sealed` on the nodes where init did not run.

**Shared tenants are short of the governing requirement too.** Their values sit
in the shared store, encrypted only under each node's barrier, so whoever holds
the node barrier can read them.

## Binding rules

- **No key transport.**
  - Root keys are never pushed or transported, and there is no single key for
    everything.
  - Each node holds its own key and seals and unseals its own data.
  - Seal, unseal and rekey are per node and are never replicated.
  - Rejected: a node delivering a root key **or an unseal credential** to
    another node.
- **Any key provider.** The design must work with every go-xkms key provider:
  software, hardware or cloud. Pushing the tenant key over `clusterkeys` and
  hiding the gap behind an auto-unseal test are both rejected.
- **Encryption seam standard.**
  - Envelope KEK/DEK behind a seam.
  - go-xkms supplies the hardware and cloud implementations, and the platform
    composes them.
  - qrdb carries no HSM, TPM or KMS code.
- **State and deployment.**
  - No node-local state except the per-node seal keys.
  - Works both standalone and under Raft.
  - Node parity: every node serves every endpoint.
- **Change rules.**
  - A new Raft opcode needs a cluster feature gate.
  - No backward-compatibility code: pre-release tenants are re-created.
  - Replaced surfaces are deleted.
- **No hand-rolled crypto.**
- **Barrier rulings.**
  - An isolated tenant's keys are independent of the system seal posture.
  - The multiplexed WAL is always built.
  - A multi-replica raft database's barrier is a per-node barrier.

## What already exists

| Prerequisite | State | Needed? |
|---|---|---|
| Home store for every isolated tenant, its Raft log on a dedicated WAL under each node's tenant barrier (S4: G1–G4, G6) | shipped | **Yes. This is the part of MT-15 this needs, and it is in place.** Tenant identity, the DEK keyring and invitations are plain Raft entries that each node encrypts at rest under its own tenant barrier, so a replica with its own key reads what another replica wrote. This removes the 2026-10-06 blocker (`quicraft/seal: decryption failed`). |
| Reconciler gates each tenant shard on a registered, unsealed barrier and the cluster seal record (`ensureStarted`) | shipped | **Yes, unchanged.** Once the barrier exists and is unsealed on each node, the replica starts and the group forms at a majority. |
| Cluster seal record (`pkg/clusterseal`, feature `cluster-seal`) | shipped | **Yes.** It names every barrier level (`NodeBarrier`, `TenantBarrier`, `DatabaseBarrier`). Unseal is checked against it, and its epoch fences a node that is cut off. |
| Proof of possession for init (`pkg/initproof`) | shipped | **Yes, extended.** Today it signs the tenant, the nonce and the TLS exporter, but not the init parameters. |
| Replicated database catalog (MT-16) | in flight | Only for the database-barrier work package. |
| Opaque database-id placement (MT-17, S5) | blocked | **No.** It changes no key path (open question 5). |
| Records still in the system shard (custodian records, SO PIN commitment, unseal roster, encryption-migration state, pending first admin) | present | **No.** They are under each node's own barrier, not a tenant key. Moving them is phase 8. |

## Options (Option A decided)

The options differ only in **who starts init on each node**. In every option,
each node generates its own random key and seals it through its own strategy.

- **A — node-local where no client credential is needed, client fan-out where
  one is. Decided.**
- **B — client fan-out for every mode.** Rejected. It leaves hardware and KMS
  joins needing a person, even though nothing secret is needed.
- **C — node-local reaction for every mode.** Rejected. It needs the
  credential to be relayed between nodes, or replicated.
- **Already rejected, not to be re-proposed:**
  - transporting the root key or the sealed blob;
  - deriving every node's key from one root with a per-node salt;
  - one shared KMS data key for every node.

## Design

### 1. One mechanism for every barrier

The **node barrier** (the system tenant's own key), the **shared group's
barrier** (one platform-managed tenant; §8), an **isolated tenant's barrier**
and a **database barrier** are all built the same way. *Decided (owner,
2026-10-08), open question 1.* Each one has:

- the same seam;
- the same signed statement;
- the same per-node init, unseal, rekey and join.

Only two things differ by barrier kind:

- **The init statement's signer** is the system tenant's first admin for the
  node barrier, and the owning tenant for a tenant or database barrier.
- **What the key protects:**
  - a node's whole data plane;
  - the shared store, for every shared tenant in it;
  - an isolated tenant's stores and dedicated WALs;
  - one database.

### 2. The seam — `BarrierKeyProvider`

`StrategyFactory` (`pkg/seal/strategy_registry.go`) takes no parameters, so it
cannot say *which* HSM key or KMS key wraps *this* barrier's key on *this*
node. One seam replaces strategy resolution for every barrier. It sits beside
`volume.KeyProvider`, `cakey.Provider` and `keywrap.Provider`, and has the same
shape as they do:

```go
// BarrierKeyProvider is the seam through which one node obtains the
// SealingStrategy that seals its own key for one barrier: the node barrier,
// a tenant barrier (shared or isolated) or a database barrier. go-qrdb ships
// the software implementation over the StrategyRegistry; a composer injects
// hardware- or KMS-backed implementations (go-xkms) through
// WithBarrierKeyProvider. Implementations must be safe for concurrent use.
type BarrierKeyProvider interface {
	// BarrierStrategy returns the strategy that seals this node's key for
	// req.Barrier, what unsealing it needs from a client, and who holds the
	// wrapping key.
	BarrierStrategy(ctx context.Context, req BarrierKeyRequest) (BarrierKeyStrategy, error)
}

// BarrierKeyRequest names one node's key for one barrier.
type BarrierKeyRequest struct {
	// Barrier is the barrier's cluster-wide name: "node", "tenant/<tenant>"
	// or "database/<tenant>/<database>" (the clusterseal.Barrier grammar).
	Barrier    string
	NodeID     string
	StrategyID StrategyID
	// KeyRef is the opaque key reference from the init statement: one
	// tenant-controlled KMS key by default, or a per-node template the
	// provider resolves (decided). Empty for the software strategies.
	KeyRef string
	// Epoch is the init statement's credential epoch.
	Epoch uint64
}

// BarrierKeyStrategy is a resolved strategy and what it needs.
type BarrierKeyStrategy struct {
	Strategy   SealingStrategy
	Credential CredentialKind // CredentialNone, CredentialPassphrase, CredentialShares
	Custody    Custody        // CustodyOwner, CustodyPlatform, CustodyNone
}
```

**`Custody` records the tenant's choice** (§9); no policy refuses on it.
`CustodyOwner` means only the
barrier's owner can release the key: its passphrase, its shares, or a KMS key
the tenant controls. A provider declares its own custody as part of its
contract. go-qrdb cannot inspect a KMS policy, so this is a statement by the
provider, which the composer vouches for.

**Default provider.** It is built over `StrategyRegistry`, which keeps its
role, and maps the built-in strategies like this:

| Strategy | Credential | Custody |
|---|---|---|
| `passphrase` | `CredentialPassphrase` | `CustodyOwner` |
| `shamir` | `CredentialShares` | `CustodyOwner` |
| `system-barrier` (platform-sealed) | `CredentialNone` | `CustodyPlatform` |
| `none` | `CredentialNone` | `CustodyNone` |

**Composition and what it replaces.**

- `WithBarrierKeyProvider` is added to `pkg/server`, `sdk/go/node` and
  `pkg/server/lifecycle`.
- It replaces the `switch` in `resolveStrategy`.
- With PN-12, it also replaces `WithSealStrategy` (`pkg/server/server.go`,
  `sdk/go/node/node.go`), whose only job is choosing the node barrier's
  strategy.

### 3. The init statement — replicated, keyless, signed

```go
// InitStatement is the replicated, signed statement of how every node's key
// for one barrier is sealed. It carries no key material and no credential
// verifier; every field is public.
type InitStatement struct {
	Barrier        string // clusterseal.Barrier grammar
	Epoch          uint64 // 1 at init; +1 per credential change
	StrategyID     StrategyID
	KeyRef         string
	Threshold      int    // Shamir only
	RosterHash     []byte // Shamir only: digest of the holder roster
	VerifierSalt   []byte // passphrase only: salt of the barrier-wide verifier
	UnsealKeyCheck []byte // Shamir only: HKDF-SHA256 check value of the unseal key
	SignerSPKI     []byte // the signing key's DER SPKI (see below)
	SignerCertificate []byte // epoch 2 and later: DER, chaining to the owner's CA
	Signature      []byte
}
```

*(Corrected 2026-10-08, as implemented in `pkg/barrierinit`: `SignerSPKI`
is the whole DER `SubjectPublicKeyInfo`, not its SHA-256. A hash cannot
verify a signature, and the epoch-1 anchor is itself a thumbprint, so the key
travels with the statement and is checked against the thumbprint. A later
epoch also carries its `SignerCertificate`, whose SPKI must equal
`SignerSPKI`.)*

**Signature.**

- The client signs with the same `crypto.Signer` it uses for
  `init/complete`. That is the seam for device-held keys.
- The signature covers a domain-separated, length-prefixed encoding of every
  other field. It follows the `initproof.Message` pattern, with its own context
  string.
- Every node verifies it before it seals anything, and so does every later
  joiner:
  - epoch 1 against the pending first admin's SPKI, recorded at tenant create
    (for the node barrier, at first boot);
  - later epochs against a certificate chaining to the owner's CA, whose public
    certificate is pinned in the system tenant.
- A statement the platform or a single compromised node writes, for example one
  naming a KMS key the platform controls, fails on every honest node with
  `InitStatementError`.
- Each version is kept, so that a joiner can verify the chain.

**Credential check values, where no oracle is involved.**

- **Shamir:** `UnsealKeyCheck` is an HKDF-SHA256 check value of a 256-bit
  unseal key. Its input is high-entropy, so it reveals nothing worth guessing
  at, and it can be replicated.
- **Passphrase:** a check value would be an offline-guessing oracle, so the
  statement carries the salt only (§6).

**Storage.**

- **Where:** in the system shard, under `__sys:cluster/barrier/init/<barrier>`.
  It is a sibling of the seal records and, like them, holds only public
  fields.
- **How it is written:** guarded create-if-absent at epoch 1, then a guarded
  compare-and-set on `Epoch`, both through the existing `OpGuarded`. It adds
  **no Raft opcode**.
- **Standalone:** the same record, in the standalone system store.
- **The node barrier's statement** reaches a joining node through the
  cluster-enroll handshake rather than the system shard. The joiner cannot read
  the system shard until its own node barrier is unsealed, and the statement is
  public and signed (§11).

### 4. Who drives init

**Shell registration.** When placement first assigns a node to a barrier's
group, the reconciler registers an **uninitialized, sealed shell** on that
node. No key exists yet. The shell is what lets status, init and unseal reach
the node.

**The first node** can be any node, so node parity holds. It:

1. verifies the init proof and the statement;
2. **claims the statement** with the guarded create, so that a concurrent init
   with different parameters loses before any key exists
   (`BarrierAlreadyInitializedError`);
3. generates its key, seals it through `BarrierStrategy`, and unseals.

**Every other node** follows its credential kind:

- **`CredentialNone`** (a tenant-controlled KMS or hardware key, or
  platform-sealed where policy allows it):
  - the node watches the statement, and when one verifies for a barrier it
    serves, it generates its own key and seals it;
  - failures retry with backoff (`BarrierKeyProviderError`);
  - a node that was down catches up when it returns.
- **`CredentialPassphrase`:** the CLI or SDK sends the signed statement and the
  passphrase to every serving node. Each node:
  - checks the statement against the claimed record;
  - computes the barrier-wide verifier `Argon2id(passphrase, VerifierSalt)` and
    stores it in its bootstrap record;
  - seals its key under `Argon2id(passphrase, its own salt)`.

  The two salts keep two domains: the verifier never opens a key.
- **`CredentialShares`:**
  - the first node generates the unseal key, splits it to the enrolled
    holders, and returns its check value, which goes into the statement the
    client signs;
  - every other node holds **no key** until a share quorum reaches it, then
    checks the reconstructed key against `UnsealKeyCheck`, generates its key,
    seals it under the unseal key (`pkg/seal/unsealkey.go`, with the roster
    digest in the AAD), and unseals;
  - each holder submits once, and its client sends the share to every serving
    node;
  - the unseal key is never relayed between nodes.

  This drops the "pending key held in memory" of the earlier design, which a
  restart lost and which bought nothing.

**Data never needs a shared root key.** Raft entries and snapshots of an
isolated tenant move as logical plaintext over node mTLS, and each node
re-encrypts them under its own barrier. A sealed node runs no replica, so it
receives none of the tenant's entries. Shared tenants work differently (§8).

### 5. Quorum and the first admin

- **When the group forms.** It forms on the nodes that have unsealed, and has
  quorum once a majority of the serving set has. Until then, every request
  except status, init and unseal is refused with `TenantHomeUnavailableError`
  (503, retryable).
- **Init returns before the admin exists.** Writing the first admin needs that
  quorum, so the init response returns `initialized`, the bound SPKI and each
  node's state. The CLI then waits for the group.
- **The leader writes the admin.** `CompletePendingTenantAdmin` runs on the
  group leader only, and is retried on every leadership change and every
  unseal.
- **Every write is guarded:**
  - the CA is create-if-absent, and a loser reads the winner's CA;
  - the user is create-if-absent;
  - the pending record is removed by a guarded delete.

  Running it twice is harmless, and no second CA is ever made.

### 6. Join and replacement

A joining node comes up sealed. It has a shell and the verified statement.

**By credential kind:**

- **`CredentialNone`:** the node self-initializes. A tenant-controlled KMS key
  works only after the tenant grants the new node's identity, and that grant is
  the tenant's consent.
- **`CredentialShares`:** holders submit their shares to the joiner, which
  checks the result against `UnsealKeyCheck`.
- **`CredentialPassphrase` (decided):** an unsealed serving node delivers the
  verifier `(VerifierSalt, tag)` to the joiner over the `clusterkeys` HPKE
  channel. That channel is placement-gated, revocation-checked and addressed
  to one node.
  - The verifier is not a key. It is an offline-guessing oracle, but so is the
    sealed blob every serving node already holds.
  - It never enters the replicated system store.

**Replacement** is a join followed by removing the old member. The old key dies
with its disk.

**Admission** — *Decided (owner, 2026-10-08), open question 6, option (a).*
Placement adds a node to a tenant's group only if the node is in the tenant's
**signed serving set**.

- **Who signs.** The tenant signs the set of nodes that may hold its key, with
  the same signer chain as its `InitStatement` (the first admin's
  `crypto.Signer` at epoch 1, a certificate chaining to the owner's CA after).
  For a tenant that chose auto-unseal (`CustodyPlatform`), the platform signs.
- **Who checks.** Placement, and every node before it registers a shell: a node
  not named in a verified set is not added and does not serve.
- **What it buys.** The tenant's consent is explicit for every credential kind,
  not only KMS. The size of the set is still the tenant's replica-count quota
  (PN-13); the signed set says *which* nodes, the quota says *how many*.
- **Cost.** One tenant action per join, which the tenant's own automation can
  do through the `crypto.Signer` seam. It stays unattended for an auto-unseal
  tenant, where the platform signs.

### 7. Unseal and rekey

**Unseal** is per node.

- **Credential:** the holder's client fans the credential out to each node
  (`--all`) and reports each node's result.
- **What a node checks:**
  - the sealed window admits only a certificate chaining to the owner's CA;
  - the bootstrap record (sealed blob, verifier, roster and statement epoch)
    is bound to the node's key (`seal.BootstrapRecordMismatchError`).
    *Implemented as AEAD encryption under the node's barrier key with the
    record's path inside the plaintext, not a separate MAC; the record holds
    the epoch and the verifier so far;*
  - the cluster seal record must not say the barrier is sealed.
- **A cut-off node** stops serving after the monitor's window and keeps its
  key.

**Rekey** comes in three kinds:

- **Key rotation:** node-local, as today's barrier epoch rotation is. Nothing
  replicates.
- **Credential change** (a new passphrase, a Shamir re-split, a new `KeyRef`):
  the client signs epoch N+1, claims it with a compare-and-set, and fans out.
  An unsealed node needs only the new credential to re-wrap its key.
- **Strategy change:** a credential change to another strategy. This is what
  **enable-encryption** becomes (*decided, SL-02*):
  - moving off `none` in place is a new signed `InitStatement` epoch, `none`
    → keyed, through `BarrierKeyProvider`; it is the same for every tenant
    type, strict included, and for a tenant that started platform-sealed
    (§9: custody is the tenant's choice);
  - a passphrase target reaches the other nodes over HPKE from a serving
    peer (§6), never through a file on the node;
  - a verified backup is a precondition only. There is no separate
    backup/restore migration path.

**A node that missed a credential change** refuses the old credential
(`StaleCredentialEpochError`). It catches up by taking the old and new
credentials together in one call. If the old credential is lost and a quorum
holds elsewhere, the operator removes the replica and the node rejoins fresh.
A stale node never wipes itself, because a stale majority would lose the
group.

### 8. Shared tenants

*(Superseded 2026-10-09: shared tenants were removed outright. Every tenant is
isolated on keys of its own, and only the system tenant, whose records live
under each node's own barrier, has no tenant key. This section is kept as the
record of the 2026-10-08 ruling.)*

**Ruled (owner, 2026-10-08): no keys of their own.** The per-tenant envelope
scheme this section proposed — per-node recipient keys, per-epoch data keys
wrapped to each serving node, values encrypted at proposal — is **rejected**,
and its work package (PN-11) and feature (`shared-tenant-keys`) are
withdrawn.

- **The shared Raft group is one more tenant** with its own barrier, which the
  platform manages through the same `BarrierKeyProvider` mechanism.
- **Tenants in it have no keys of their own.** Their values are encrypted at
  rest under that platform-managed barrier only.
- **A shared tenant's data is readable by the platform.** This is stated, not
  hidden: a tenant that needs confidentiality from the platform chooses an
  isolated type (`barrier`, `volume` or `raft`).

### 9. Key custody

**Ruled (owner, 2026-10-08): auto-unseal is the tenant's choice.** The
custody policy this section proposed — refusing a platform-sealed key under
`strict` with `PlatformSealedKeyError` — is **rejected**; no such error
exists.

- `Custody` records the tenant's opt-in: a platform-sealed key
  (`CustodyPlatform`) is what a tenant that wants auto-unseal chooses, under
  either isolation policy. The `AllowSystemManagedBarrier` entitlement still
  gates the choice.
- The tenant's barrier status shows the custody, so the choice is auditable.
- qrdb ships no native hardware or cloud crypto, only seams. `KeyRef` is
  opaque to qrdb: an injected strategy (go-xkms) reads it.

### 10. The database barrier

A multi-replica raft database with its own barrier uses the same machinery,
keyed by `clusterseal.DatabaseBarrier(tenant, database)`. It reads the
database's barrier settings from the replicated catalog (PN-9).

*Implemented (PN-9):*

- The barrier encrypts the state machine of the database's own group on each
  replica, in place of the tenant's; the log and snapshots stay under the
  tenant's barrier.
- The create carries the epoch-1 statement, signed by a tenant admin under the
  tenant's CA (`barrierinit.SignDatabaseInit`, CLI `--barrier-signer-cert`).
  A database has no first admin of its own, so its statement verifies against
  the tenant's CA at every epoch, and the platform never signs one.
- The accepting node claims the statement and holds the passphrase. Every
  replica placed in the database's group and admitted by the tenant's serving
  set fetches the passphrase through that group and makes its own key; no key
  is transported.
- Refused: a Shamir raft database barrier (a split is one node's key) and any
  barrier of a system-tenant raft database (no owner to sign), both with
  `database.RaftBarrierUnsupportedError`.
- Proved by `TestDatabaseBarrierPerNode_CLI_PerProtocol`: three nodes, the real
  CLI on every transport, every replica serving, three distinct fingerprints.
- Not covered here and left to PN-10: restart (each replica's barrier comes up
  sealed and is unsealed on its node) and scale-up past the passphrase hold.

### 11. The system barrier folded in

*Decided (owner, 2026-10-08), open question 1.*

The node barrier is already per node, so nothing about its keys moves. What
changes is how it is resolved and recorded:

- **Resolution.** Its strategy is resolved through `BarrierKeyProvider`, with
  `Barrier = "node"`.
- **Statement.** At first boot, the system tenant's first admin signs its
  statement, through the same `crypto.Signer` seam firstboot already uses.
- **A joining node:**
  - receives that statement through the cluster-enroll handshake;
  - verifies it against the system tenant's first-admin SPKI or CA, pinned in
    its join trust bundle;
  - initializes its own node barrier by credential kind, exactly as a tenant
    joiner does.
- **Deletions.** `WithSealStrategy` and the node-barrier branch of strategy
  selection are deleted. `AGENTS.md`'s composition list is updated in the same
  change.

### 12. Failure modes

| Failure | Behaviour | Recovery |
|---|---|---|
| Two inits race with different parameters | the guarded claim admits one; the other is refused with `BarrierAlreadyInitializedError` before any key exists | none needed |
| First node crashes after the claim | the statement exists and that node is uninitialized | re-run init; it is idempotent against the same statement |
| Fan-out reaches only some nodes | each node's result is reported; the group forms at a majority | re-run; nodes already done return success |
| Key provider down on one node | that node stays sealed (`BarrierKeyProviderError`) and retries | none while a majority holds |
| Majority sealed or down | `TenantHomeUnavailableError` (503) | unseal more nodes |
| Forged or tampered statement | `InitStatementError`; nothing is sealed | investigate; that node stays sealed |
| Tampered bootstrap record | `seal.BootstrapRecordMismatchError`; the node seals | restore, or rejoin fresh |
| Statement epoch skipped, repeated or raced | `EpochConflictError` naming the recorded epoch; nothing written | re-read the record and sign the next epoch |
| Guarded writes not yet active | `GuardedWritesUnavailableError`; nothing written | retry after the rollout |
| Node cut off from the system shard | it stops serving after the monitor window and keeps its key | reconnect |
| Owner credential lost | the data is unrecoverable, as today; Shamir has its recovery domain | — |

### 13. Feature gates and Raft impact

- **No new Raft opcode.** Statements use `OpGuarded`, and the first admin
  uses guarded writes.
- **`per-node-barrier-keys`** is activation-based, like `cluster-seal`.
  - A binary without it would wait for a key delivery that never comes.
  - Until activation, creating an isolated tenant on a cluster of more than one
    member is refused with `NotActiveError` (503). A single-member cluster
    activates at once.
  - After activation, `AdmitReplica` refuses a node that does not advertise
    the feature.
  - *Implemented (PN-2):* until activation an isolated create, a tenant
    update to an isolated type, and recording a statement are each refused
    with `NotActiveError{Feature: per-node-barrier-keys}`. A single-member
    cluster activates at its first check; a standalone node gates nothing.
- *(Withdrawn 2026-10-08: `shared-tenant-keys` went with PN-11; see §8.)*

### 14. What is deleted

| Where | What |
|---|---|
| `pkg/clusterkeys` | `DistributeTenantKey`, `deliverSystemManaged`, `deliverSelfManaged`, the receiver's import modes and their mode table. The package keeps node identity, the HPKE envelope and the decided verifier mode. |
| `pkg/seal` | `ExportRootKeyContext`, `ImportRootKey`, `ExportSealedBlob`, `ImportSealedBlob` (`import_key.go`); `BarrierRegistry.ImportTenant`, `importTenantKey`. |
| `pkg/service` | `KeyDistributor`, `deliverTenantKeyToReplica`, the key step in `inviteReplicas`, the rebalance deliverer (`rebalance_options.go`). |
| `pkg/tenant` | the `resolveStrategy` switch. |
| `pkg/server`, `sdk/go/node` | `WithSealStrategy` (PN-12). |
| `pkg/clustermig` | the tenant arm, tenant `NodeMigrationStatus`, the tenant credential files (`seal.migration_credentials_dir`). |
| interim refusal | `StrictEncryptionMigrationError`. |

`pkg/keydelivery` stays, because the ceremony envelopes use it too.

*Implemented (PN-6):* the `pkg/clusterkeys`, `pkg/seal` and `pkg/service` rows
are deleted outright, with no shim. The cluster peer plane is
`POST /v1/cluster/verifier` alone: `/v1/cluster/keydelivery` and
`/v1/cluster/nodekey` are no longer routes, and `pkg/keydelivery` carries only
the `passphrase-verifier` mode. *(PN-8 adds `POST /v1/cluster/passphrase` and
the `strategy-passphrase` and `node-passphrase` modes, and implements the
`pkg/clustermig` and interim-refusal rows: both are deleted outright.)* Scale-up and rebalance persist the placement
and admit the new replica as an observer; the joiner initializes its own key
once the signed serving set names it.

## Limits

What this design does not reach, stated plainly:

- **The key is in a serving node's memory while it is unsealed.** Anyone who
  controls that node's memory controls that node's copy, and also sees the
  plaintext the node is handling.
  - **What protects it today:** go-enclave. Shares are already held in
    go-enclave `ProtectedShare`, and the barrier key is to be held the same way
    under the go-enclave-everywhere ruling.
  - **The full answer:** release bound to hardware or a TEE, through go-xkms
    providers. The key is unwrapped only inside attested hardware, on a node
    whose measured state the tenant's policy accepts. This design is the seam
    for that, not the implementation.
- **The guarantee assumes honest node software.** It holds against the
  platform's reach through every qrdb interface, and against its disks, WALs,
  snapshots and backups.
  - It does not hold against a platform that runs modified node binaries on a
    serving member: that member receives what Raft sends it.
  - Attested membership is the full answer, as above.
- **The platform still controls availability.** It can stop or withhold an
  isolated tenant's service, but cannot read it.
- **Shared tenants are readable by the platform** (§8): their data is under a
  platform-managed barrier.
- **Some metadata stays visible:** isolated-tenant database names in system
  records until phase 8.

## Work packages

Each package is sized for one lane, opens with a red test, and updates
`docs/multi-tenancy/` and `docs/barrier-seal/` in the same change.

| WP | Delivers | Red test | Depends on |
|---|---|---|---|
| **PN-1 Seam** | `BarrierKeyProvider`, `BarrierKeyRequest`, `BarrierKeyStrategy`, `CredentialKind`, `Custody`; the default provider over `StrategyRegistry`; `WithBarrierKeyProvider` on server, lifecycle and the SDK node; it replaces the `resolveStrategy` switch | an injected provider seals a tenant key with `KeyRef` and node passed through; an unknown strategy returns `InvalidStrategyError` | — |
| **PN-2 Statement and gate** *(shipped)* | `InitStatement` encoding and signature verification (first-admin SPKI, owner CA); the guarded claim and compare-and-set; a watcher; the `per-node-barrier-keys` feature. No custody policy (§9, ruled) | a statement signed by another key is refused with `InitStatementError`; of two concurrent claims, one wins; an isolated create on 3 nodes before activation is refused with `NotActiveError` | PN-1 (by name only: PN-2 needs none of its types) |
| **PN-3 Shells and node-local init** | Shell registration on placement; self-init for `CredentialNone`; platform-sealed tenants (the tenant's auto-unseal opt-in) on per-node keys; delete the platform-sealed transport | a platform-sealed raft tenant on 3 nodes serves on every node, **and two replicas' key fingerprints differ** | PN-2 |
| **PN-4 Fan-out init** | the statement signed by the init `crypto.Signer`; SDK and CLI fan-out with a result per node; first-admin completion on the leader, with guarded writes; on six transports and the CLI | the create step of `TestClusterEnableEncryption_TenantScope_RF3`, rewritten for strict: a passphrase raft tenant inits on 3 nodes and its group forms | PN-3 |
| **PN-5 Shamir per node** | the check value; holder share fan-out; the key generated at quorum | a Shamir raft tenant on 3 nodes unseals from one submit per holder; a quorum from the wrong share set fails the check | PN-4 |
| **PN-6 Verifier, joiner and serving set** | bootstrap records; the HPKE verifier mode; joiner init for every credential kind; the tenant-signed serving set (signed with the statement's signer chain, the platform signing for an auto-unseal tenant) and placement admitting only nodes in it; delete the tenant-sealed transport and the `pkg/seal` export and import functions | scale-up to 4 nodes: a KMS joiner serves once granted; a passphrase joiner refuses a wrong passphrase and accepts the right one; placement refuses to add a node absent from the signed set | PN-4 |
| **PN-7 Unseal and rekey** | unseal and rekey fan-out (`--all`); epochs; `StaleCredentialEpochError` and the old-and-new catch-up | change the passphrase with one node down: that node refuses the old passphrase, then the catch-up re-wraps it | PN-6 |
| **PN-8 Enable-encryption (SL-02 folded in)** *(shipped)* | moving off `none` in place as a strategy-change statement (a new signed epoch, `none` → keyed, through `BarrierKeyProvider`, for every tenant type including strict); the passphrase reaching other nodes over HPKE from a serving peer; a verified backup as a precondition only, with no backup/restore migration path; delete the `clustermig` tenant arm, `seal.migration_credentials_dir` and the credential files, and `StrictEncryptionMigrationError` | a tenant moves from `none` to `passphrase` on 3 nodes with no credential directory, strict included; the move is refused without a verified backup | PN-7 |
| **PN-9 Database barrier** *(shipped)* | the same design under `DatabaseBarrier` | a barrier-encrypted raft database on 3 nodes: every replica serves, and the fingerprints differ | PN-8, replicated catalog |
| **PN-10 Cluster suite** | for every credential kind: distinct fingerprints, cross-key snapshot catch-up, restart, scale-up and replacement; the replica-forwarding and mixed-cluster suites green; CLI tests on every transport | the named suites, which today fail with `tenant: barrier is sealed` | PN-6 |
| ~~PN-11 Shared tenants~~ | **Withdrawn (owner, 2026-10-08):** shared tenants have no keys of their own (§8) | — | — |
| **PN-12 System barrier** | node-barrier resolution through `BarrierKeyProvider`; the firstboot-signed statement; statement delivery at cluster enroll; delete `WithSealStrategy` and update `AGENTS.md` | a joiner given a node statement signed by another key refuses to initialize; a 3-node first boot runs through the seam | PN-2 |
| **PN-13 Replica count as a tenant quota** | the tenant's serving set is its replica count, a tenant quota in the existing quota and entitlement records, set by the system owner or a system admin and used by `ProvisionTenantHome` and placement | a tenant with a replica quota of 3 on a 5-node cluster gets a 3-node serving set, and init fans out to those 3 only; the tenant's own admin changing it is refused | PN-4 |

**Order.**

- PN-1 and PN-2 can run in parallel.
- PN-3 is the first package a user sees.
- PN-4 closes the RF3 create.
- PN-12 and PN-13 can run alongside PN-7 to PN-10 once their dependencies
  land.

## Open questions

**None remain open.** Questions 1 to 6 and 8 were ruled by the owner on
2026-10-08, and question 7 was withdrawn.

1. **One barrier mechanism for every barrier.** *Decided (owner, 2026-10-08):
   (a).*
   - One mechanism: the seam renamed `BarrierKeyProvider` covers node,
     isolated-tenant and database barriers, and the system barrier is folded
     in (PN-12, which deletes `WithSealStrategy`).
2. **Platform-sealed keys.** *Decided (owner, 2026-10-08): neither proposed
   option.*
   - Auto-unseal is the tenant's choice. There is no strict-mode refusal and
     no `PlatformSealedKeyError`; the statement's `Custody` field records the
     tenant's opt-in and the auditable status shows it (§9).
3. **Shared tenants on their own keys.** *Decided (owner, 2026-10-08):
   rejected.*
   - The shared Raft group is one more tenant with its own platform-managed
     barrier. Tenants in it have no keys of their own, and their data is
     readable by the platform. A tenant that needs confidentiality chooses an
     isolated type (§8). PN-11 is withdrawn.
4. **Serving-set size.** *Decided (owner, 2026-10-08): replaced.*
   - The serving set is the tenant's replica count, a tenant quota the system
     owner or a system admin sets through the existing quota and entitlement
     records. A tenant admin cannot change its own (PN-13).
5. **Drop MT-17 as a prerequisite of MT-18.** *Decided (owner, 2026-10-08):
   (a).*
   - MT-18 depends on MT-15, which is in place, and on the cluster seal record.
6. **A tenant-signed serving set.** *Decided (owner, 2026-10-08): (a).*
   - The tenant signs the set of nodes that may hold its key, with the same
     signer chain as its statement; placement adds members only for nodes in
     the set. For a tenant that chose auto-unseal, the platform signs. Folded
     into PN-6 (§6, Admission).
   - Rejected: (b), trusting platform placement and relying on
     [Limits](#limits), because placement is platform-controlled and the
     platform could otherwise add a member that receives the tenant's
     statement.
7. **Apply-time computation on shared-tenant values.** *Withdrawn with
   question 3.*
   - Shared tenants are under a platform-managed barrier, so the shared state
     machine reads their values as it does today.
8. **Moving off `none` in place (SL-02).** *Decided (owner, 2026-10-08): folded
   into this design.*
   - A new signed `InitStatement` epoch, `none` → keyed, through
     `BarrierKeyProvider`, for every tenant type including strict. The
     passphrase reaches other nodes over HPKE from a serving peer.
     `seal.migration_credentials_dir` is deleted (no node-local state). A
     verified backup is a precondition only; there is no separate
     backup/restore migration path. Work package PN-8 (§7).
