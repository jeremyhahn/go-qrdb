# Shamir Custody — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Exported surface

`sdk/go`:

```go
func (c *Client) CustodianGroups() transport.CustodianGroupClient
func (c *Client) Barrier()         transport.BarrierClient   // quorum operations
```

Composition-plane wiring (Tier 2):

```go
SetCeremonyDistributor(d ...)   // required before InitializeShamir / Rekey
SetRecoveryDistributor(d ...)   // required for recovery key generation
```

`StorageBarrier` quorum operations (`pkg/seal`):

```go
func (b *StorageBarrier) InitializeShamir(ctx, creds) (ShamirParams, error)
func (b *StorageBarrier) UnsealWithShare(ctx, share) (QuorumProgress, error)
func (b *StorageBarrier) UnsealWithShares(ctx, shares) error
func (b *StorageBarrier) SubmitSealVote(ctx, share) (QuorumProgress, error)
func (b *StorageBarrier) Rekey(ctx, threshold, total) (ShamirParams, error)
func (b *StorageBarrier) GenerateRecoveryKeys(ctx, threshold, total) (RecoveryResult, error)
func (b *StorageBarrier) RecoverWithKeys(ctx, shares) error
func (b *StorageBarrier) GenerateRootToken(ctx, shares) (RootToken, error)
```

Custodian record (`pkg/custodian`), persisted at `__sys:custodian/<custodian_id>`:

```go
type Custodian struct {
    ID              string           // UUID v4, assigned at enrollment
    Username        string           // Human-readable identifier
    Method          string           // "pkcs11" | "piv" | "fido2"
    PublicCredential []byte          // Public key / credential metadata (no private key)
    EnrolledAt      time.Time        // Enrollment timestamp
    RevokedAt       *time.Time       // nil if active; set to time of revocation
    ShareIndex      int              // Position in Shamir split (0-indexed)
    CertificateID   string           // Reference to issued certificate (if MCA-signed)
}
```

**Public**: Persisted and Raft-replicated. Does NOT contain any share material.

**Private** (server-held, temporary): Custodian's encrypted share envelope, held in memory only during the enrollment transaction. Zeroed and discarded after delivery to the Custodian.


## Initialization


`InitializeShamir` generates a root key, seals it, and — in direct mode —
applies **unseal-key indirection** (Vault-style): a random 256-bit unseal key
(UK) is generated, the root key is AEAD-wrapped (AES-256-GCM) under the UK and
persisted as a sibling blob (`<RootKeyPath>.wrapped`), and the **UK — not the
root key —** is split into N shares and distributed to the enrolled officers'
enclaves. The wrapped blob is bound to the barrier's root-key path through the
AEAD associated data, so it cannot be transplanted between barriers or
tenants. The response is **share-free**:

```go
params, err := barrier.InitializeShamir(ctx, seal.Credentials{})
// params.Threshold: 3
// params.TotalShares: 5
// params.ShareCount: 5 (shares sealed into officer enclaves)
```

A `CeremonyDistributor` must be wired via `SetCeremonyDistributor` before
initialization; otherwise `ErrCeremonyDistributorNotConfigured` is returned.
The barrier is unsealed after initialization.


## Unsealing

### Stateful (one-at-a-time)

`UnsealWithShare` accumulates shares in an internal quorum accumulator with a configurable TTL (default **15 minutes**; set `seal.share_ttl` / `QRDB_SEAL_SHARE_TTL`):

```go
progress, _ := barrier.UnsealWithShare(ctx, shareA)
// progress.Required: 3, progress.Submitted: 1, progress.Complete: false

progress, _ = barrier.UnsealWithShare(ctx, shareB)
// progress.Submitted: 2, progress.Complete: false

progress, _ = barrier.UnsealWithShare(ctx, shareC)
// progress.Submitted: 3, progress.Complete: true — barrier unsealed
```

Duplicate shares are rejected (`ErrShamirDuplicateShare`). The accumulator resets after TTL expiry (`ErrShamirQuorumExpired`).

**Share custody:** submitted shares are held only inside **go-enclave sealed
enclaves** (never as raw, garbage-collected strings) — deduplication is done
against a SHA-256 digest so the raw share is never retained as a map key, and
plaintext is materialized only transiently during reconstruction. The instant
the quorum reconstructs the secret, every accumulated share enclave is
destroyed and the reconstructed secret is wiped after the unseal consumes it —
so a subsequent unseal requires the Security Officers to submit fresh shares.
The session TTL bounds how long un-completed shares may linger
(`seal.share_ttl`, default 15m).

### Stateless (batch)

```go
err := barrier.UnsealWithShares(ctx, shares[:3])
```


## Sealing requires a quorum

A Shamir-strategy barrier cannot be sealed by a single administrator. `Seal`
returns `ErrShamirBarrierRequiresQuorum`; instead, M officers each submit
their share as a **seal vote**:

```go
progress, _ := barrier.SubmitSealVote(ctx, shareA)
// progress.Required: 3, progress.Submitted: 1, progress.Complete: false

progress, _ = barrier.SubmitSealVote(ctx, shareB)
progress, _ = barrier.SubmitSealVote(ctx, shareC)
// progress.Complete: true — quorum verified, barrier sealed
```

The reconstructed secret is verified against the barrier's key material
before sealing (`ErrSealVoteVerificationFailed` on mismatch) and is wiped
immediately afterward. The vote accumulator follows the same enclave custody
and TTL rules as the unseal accumulator. This quorum gate applies **only** to
Shamir-strategy barriers — `none` and `passphrase` barriers seal directly.


## Rekey

Generate a fresh unseal key and share set without changing the root key:

```go
params, err := barrier.Rekey(ctx, 4, 7) // new 4-of-7 split
// New shares are sealed to the enrolled officers' enclaves; unclaimed
// enclaves from the prior round are destroyed and claim history resets,
// so each officer must claim again. The response carries only
// Threshold/TotalShares/ShareCount.
```

**Rekey cryptographically revokes every previously distributed share —
including shares officers have already claimed.** A new unseal key is
generated, the root key is re-wrapped under it, and the re-wrapped blob
replaces the old one. A prior-round quorum reconstructs an unseal key that no
longer decrypts the wrapped root key, so stale shares fail with
`ErrUnsealKeyMismatch` at unseal, seal-vote, and root-token generation. The
root key itself never rotates (the epoch barrier derives per-epoch DEKs from
it) and no stored data is re-encrypted.

**The unwrapped root key never touches ordinary heap memory.** The AEAD
decrypts the wrapped blob in place into a `pkg/secure` protected buffer sized
exactly to the plaintext, and the unseal path hands that buffer to the
recovery strategy by consuming it (`secure.NewSecretFromBuffer`). An AEAD that
returns a slice it allocated itself instead of the destination it was given is
refused with `UnprotectedDecryptError` and its output is wiped. Legacy direct
mode, where the reconstructed secret is the root key, copies it into a
protected buffer too. The single resolution step shared by `UnsealWithShare`
and `UnsealWithShares` is unexported: the only way to it is through the
barrier's own quorum accumulator.

Ordering is crash-safe: the new shares are distributed *before* the
re-wrapped blob is persisted. If distribution fails, the old blob — and thus
the old shares — remain valid and `Rekey` can simply be re-run; there is no
window where old shares are dead with new shares undistributed. Legacy
barriers (initialized before unseal-key indirection) are upgraded by their
first `Rekey`, after which the original root-key shares are revoked.


## Recovery keys

Recovery keys are an independent Shamir split of the root key (not DEK) for
disaster recovery. Recovery shares follow the same SO distribution model
through a dedicated recovery distributor (`SetRecoveryDistributor`) and
recovery officer registry; they are never returned in the generation response
and never persisted on the server.

```go
// Generate (barrier must be unsealed); response is share-free.
result, err := barrier.GenerateRecoveryKeys(ctx, 3, 5)
// result.Threshold: 3, result.TotalShares: 5, result.ShareCount: 5

// Recover (barrier must be sealed) using shares claimed by recovery officers.
err = barrier.RecoverWithKeys(ctx, recoveryShares[:3])
```


## Root token generation

`GenerateRootToken` produces a one-time admin token by proving knowledge of
the current unseal key through share reconstruction (the reconstructed key
must decrypt the wrapped root key; rekey-revoked shares are rejected).
Officers submit their claimed shares as a deliberate consent signal — this is
the only sanctioned use of raw shares as request input, and no token is
minted at initialization time:

```go
token, err := barrier.GenerateRootToken(ctx, shares[:3])
// token.Token: hex-encoded HMAC-SHA256 value
```


## Invariants

- **Shares are never in a response.** Initialization, rekey and recovery
  generation all return share-free results carrying only
  `Threshold`/`TotalShares`/`ShareCount`.
- **A share is claimed exactly once**; the server enclave is destroyed on
  success.
- **Claim nonces carry a 5-minute TTL.**
- **Claim routes are identity-gated, not RBAC-gated.** Officers are ceremony
  principals registered before the user store exists. The handler enforces
  registry membership, certificate-thumbprint match and nonce-signature
  verification.
- **Duplicate shares are rejected** (`ErrShamirDuplicateShare`), deduplicated
  by SHA-256 digest so the raw share is never a map key.
- **The quorum accumulator has a TTL** — default **15 minutes**, configurable
  as `seal.share_ttl` / `QRDB_SEAL_SHARE_TTL`. Expiry resets it
  (`ErrShamirQuorumExpired`).
- **Every accumulated share enclave is destroyed the instant the quorum
  reconstructs**, and the reconstructed secret is wiped after use — so a
  subsequent unseal needs fresh shares.
- **Rekey is crash-safe by ordering**: new shares are distributed *before* the
  re-wrapped blob is persisted. If distribution fails, the old blob and old
  shares remain valid and `Rekey` can simply be re-run. **There is no window in
  which old shares are dead and new shares undistributed.**
- **Rekey does not rotate the root key** and re-encrypts no stored data; the
  epoch barrier derives per-epoch DEKs from the unchanged root key.
- **Rekey resets claim history** and destroys unclaimed enclaves from the prior
  round — every officer must claim again.
- **Threshold must be ≥ 2 and ≤ total** (`ErrShamirThresholdInvalid`).
- **A Shamir barrier cannot be sealed by one administrator**
  (`ErrShamirBarrierRequiresQuorum`); `none` and `passphrase` barriers seal
  directly.
- **Seal votes are verified before sealing** (`ErrSealVoteVerificationFailed`)
  and the reconstructed secret is wiped immediately after.
- **Recovery keys are an independent split of the root key** (not the DEK, not
  the unseal key), generated with the barrier **unsealed** and used with it
  **sealed**. They are never returned in the generation response and **never
  persisted on the server**.
- **`GenerateRootToken` is the only sanctioned use of raw shares as request
  input.** No token is minted at initialization.

## Error reference

| Error | Condition |
|-------|-----------|
| `ErrShamirNotConfigured` | `BarrierConfig.Shamir` is nil |
| `ErrShamirThresholdInvalid` | Threshold < 2 or > total |
| `ErrShamirDuplicateShare` | Same share submitted twice |
| `ErrShamirQuorumExpired` | share TTL elapsed (default 15m, `seal.share_ttl`) |
| `ErrShamirQuorumIncomplete` | Not enough shares provided |
| `ErrShamirCombineFailed` | Share reconstruction failed |
| `ErrShamirUnsealRequiresQuorum` | Shamir strategy asked to unseal outside the share quorum |
| `ErrShamirBarrierRequiresQuorum` | Single-admin `Seal` attempted on a Shamir barrier |
| `ErrSealVoteVerificationFailed` | Seal-vote quorum reconstructed a secret that fails verification |
| `ErrCeremonyDistributorNotConfigured` | Init/rekey attempted before `SetCeremonyDistributor` |
| `ErrUnsealKeyMismatch` | Reconstructed unseal key failed to decrypt the wrapped root key (wrong shares, or shares revoked by a prior rekey) |
| `ErrWrappedRootKeyCorrupt` | Wrapped root key blob is malformed (bad JSON, version, algorithm, or nonce) |
| `ErrRecoveryKeysNotFound` | No recovery key metadata |
| `ErrRootTokenVerificationFailed` | Share verification failed |

## Configuration

| Key | Env | Default | Meaning |
|---|---|---|---|
| `seal.share_ttl` | `QRDB_SEAL_SHARE_TTL` | `15m` | how long un-completed accumulated shares linger |

Threshold and total are per operation (`init-shamir`, `rekey`,
`recovery generate`), not config keys.

## Persistence

- **Custodian records**: `__sys:custodian/<custodian_id>` — public metadata,
  Raft-replicated, **no share material**.
- **Wrapped root key**: `<RootKeyPath>.wrapped`, AEAD-bound to the root-key
  path through associated data.
- **Officer enclaves**: go-enclave sealed enclaves, in memory only, destroyed on claim or rekey.
- **Recovery shares**: nowhere on the server.

**Compatibility**: barriers predating unseal-key indirection have no wrapped
blob and run in legacy direct mode; the first `Rekey` upgrades them and revokes
the original root-key shares.

## Test strategy

- **Unit**: share accumulation and progress reporting, duplicate rejection by
  digest, TTL expiry, quorum reconstruction, `ErrUnsealKeyMismatch` on
  rekey-revoked shares, seal-vote verification, and the rekey ordering
  guarantee (distribution before persistence).
- **Zeroization is a tested property**, not a comment: enclave destruction on
  quorum and after use.
- **No `time.Sleep`** — the accumulator TTL is driven through the clock seam.
- **Error tests** for every row of the error reference above.

## Observability

Barrier audit events cover the custody operations — `initialize`, `unseal`,
`seal`, `rekey`, `recovery_generate` — with `allow`/`deny` outcomes. Per-officer
enrolment and claim state is reported by the claim-status endpoint, which
**never** returns share bytes. Custodian-specific audit events are listed in
[seal/custodians.md](../seal/custodians.md#audit-events).
