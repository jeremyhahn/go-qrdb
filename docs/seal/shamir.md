<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Shamir Threshold Unsealing

The StorageBarrier supports M-of-N Shamir secret sharing for root key custody.
Share custody follows the **Security Officer (SO) distribution model**: shares
are never returned from initialization or rekey responses. Instead, each share
is sealed into a server-side `secure.Secret` (go-enclave `SealedEnclave`)
bound to an enrolled Security Officer — AES-256-GCM ciphertext on the
ordinary heap at rest, mlocked only for the instant a claim opens it — who
retrieves it exactly once through an mTLS-authenticated claim flow. See
[so-share-distribution-design.md](so-share-distribution-design.md)
for the full design contract and invariants.

## Security Officer Lifecycle

1. **Register** — a system administrator submits each officer's CSR
   (`POST /v1/barrier/officers`). The ceremony CA signs it and binds the
   officer to the SHA-256 thumbprint of the issued certificate. The response
   carries the signed certificate and the officer's share index — never share
   bytes. When `qrdbd` manages its own TLS material, the ceremony CA is a
   `ceremony.LocalCA` built from the cluster CA bundle, so officer
   certificates chain to the same CA the server trusts for mTLS client auth
   (see [sealed-window.md](sealed-window.md)); with BYO TLS certificates,
   registration requires the full initialization ceremony to establish a CA
   first.
2. **Initialize** — `InitializeShamir` splits the secret and seals one share
   per enrolled officer into a `secure.Secret` (heap ciphertext at rest, per
   the custody note above) via the wired `CeremonyDistributor`.
3. **Claim** — each officer authenticates with their own mTLS certificate and
   performs the two-step claim:
   `POST /v1/barrier/officers/{username}/claim/begin` returns a nonce
   (5-minute TTL); the officer signs it with their private key and submits
   `POST /v1/barrier/officers/{username}/claim/complete`. The share bytes are
   returned exactly once and the server-side enclave is destroyed.
   The claim routes are **identity-gated** (`permission.IdentityGated`), not
   RBAC-gated: officers are ceremony-registry principals, not RBAC users (the
   user store does not yet exist when they are registered), so the middleware
   requires only an authenticated mTLS identity and the handler enforces the
   real authorization — registry membership, certificate-thumbprint match
   against the enrollment record, and nonce-signature verification.
4. **Status** — `GET /v1/barrier/officers/claim-status` reports per-officer
   enrollment and claim state (never share bytes).

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

## Dual-Mode Operation

| Mode | Strategy | Shamir Secret | Use Case |
|------|----------|---------------|----------|
| **Direct** | `StrategyShamir` | Unseal key (AEAD-wraps the root key) | Multi-party root key custody |
| **Credential** | Any other strategy | Credential password | Password-based unsealing with quorum |

In direct mode the reconstructed quorum secret is authenticated by decrypting
the wrapped root key blob: wrong, garbage, or rekey-revoked shares fail the
AEAD open with `ErrUnsealKeyMismatch` instead of silently unsealing with a
wrong key. Barriers initialized before unseal-key indirection have no wrapped
blob; for those the reconstructed secret is the root key itself (legacy
direct mode) and the first `Rekey` upgrades them in place.

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

**Share custody:** submitted shares are held only inside **go-enclave
locked-memory enclaves** (never as raw, garbage-collected strings) — deduplication is done
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

## Sealing Requires a Quorum

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

Ordering is crash-safe: the new shares are distributed *before* the
re-wrapped blob is persisted. If distribution fails, the old blob — and thus
the old shares — remain valid and `Rekey` can simply be re-run; there is no
window where old shares are dead with new shares undistributed. Legacy
barriers (initialized before unseal-key indirection) are upgraded by their
first `Rekey`, after which the original root-key shares are revoked.

## Recovery Keys

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

## Root Token Generation

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

## Error Reference

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
