# Shamir Custody — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

M-of-N custody of the barrier's key material: Security Officers, share
distribution, quorum unseal, quorum seal, rekey, recovery keys and root tokens.

The barrier itself is [../barrier-seal/](../barrier-seal/).

## Problem

A single passphrase means one person — or one compromised secret — can open the
whole store. Splitting the key is the easy half; the hard half is getting each
share to the right person without the server ever handing shares out to whoever
asks, and without a share sitting in a log, a response body, or a
garbage-collected string.

## Shape chosen

**Shares are never returned from initialization or rekey.** Each share is
sealed into a server-side `secure.Secret` (go-enclave sealed enclave) bound to an enrolled Security
Officer, who retrieves it **exactly once** through an mTLS-authenticated
challenge/response claim. The full design contract is
[seal/so-share-distribution-design.md](../seal/so-share-distribution-design.md).

### Security Officer lifecycle


1. **Register** — a system administrator submits each officer's CSR
   (`POST /v1/barrier/officers`). The ceremony CA signs it and binds the
   officer to the SHA-256 thumbprint of the issued certificate. The response
   carries the signed certificate and the officer's share index — never share
   bytes. The certificate's identity comes from the registration, not the
   CSR: it is issued as `CN=<username>`, `OU=[<scope tenant>, custodian]`
   with no Subject Alternative Name, where the scope tenant is `system` for
   the node barrier and recovery scope and the tenant itself for a
   tenant-barrier officer. Whatever Subject or SANs the CSR asks for are
   ignored, and the certificate is a holder certificate the authentication
   middleware never resolves to a user account, so registering an officer
   cannot mint a login for anyone (see
   [../certificate-authority/architecture.md](../certificate-authority/architecture.md)).
   When `qrdbd` manages its own TLS material, the ceremony CA is a
   `ceremony.LocalCA` built from the cluster CA bundle, so officer
   certificates chain to the same CA the server trusts for mTLS client auth
   (see [sealed-window.md](../seal/sealed-window.md)); with BYO TLS certificates,
   registration requires the full initialization ceremony to establish a CA
   first.
2. **Initialize** — `InitializeShamir` splits the secret and seals one share
   per enrolled officer into a `secure.Secret` via the wired
   `CeremonyDistributor`.
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

### Unseal-key indirection

Initialization does **not** split the root key directly. A random 256-bit
**unseal key** is generated, the root key is AEAD-wrapped (AES-256-GCM) under
it and persisted as a sibling blob (`<RootKeyPath>.wrapped`), and the **unseal
key** — not the root key — is split into N shares. This is Vault-style
indirection, and it is what makes rekey possible without re-encrypting data.

The wrapped blob is bound to the barrier's root-key path through the AEAD
associated data, **so it cannot be transplanted between barriers or tenants.**

The reconstructed quorum secret is authenticated by decrypting that blob:
wrong, garbage or rekey-revoked shares fail the AEAD open with
`ErrUnsealKeyMismatch` rather than silently unsealing with a wrong key.

Barriers initialized before indirection have no wrapped blob; for those the
reconstructed secret *is* the root key (legacy direct mode), and the first
`Rekey` upgrades them in place.

## Two tiers of custody

### Two-Tier Share Architecture

```
Root Key (256-bit)
  |
  +--- Operational Shamir (M-of-N)
  |      |
  |      +--- Share 1 → Custodian A (PIV card, encrypted envelope)
  |      +--- Share 2 → Custodian B (FIDO2 authenticator, encrypted envelope)
  |      +--- Share 3 → Custodian C (PKCS#11 token, encrypted envelope)
  |      
  |      Unseal: M-of-N shares + challenge-response → Root Key reconstructed
  |
  +--- Recovery Keys (separate offline escrow)
         |
         +--- Key 1 → Secure vault (offline, air-gapped)
         +--- Key 2 → HSM backup location (with dual-access control)
         +--- Key 3 → Corporate backup system
         
         Unseal: M-of-N recovery keys (different M-of-N than operational) → Root Key reconstructed
```

**Operational Custodians** hold Shamir shares paired with hardware auth factors (PIV, FIDO2, PKCS#11). They participate in daily unseal ceremonies and must be online and responsive.

**Recovery Key Holders** are a **separate, offline tier**. Recovery Keys are generated independently via `BarrierGenerateRecoveryKeys()` and are never persisted in the qrdb system (instead held in external secure storage: vault, HSM backup, etc.). Recovery Keys are used only in disaster-recovery scenarios when the quorum of Operational Custodians is unavailable.

**Critical invariant**: No single Custodian holds the complete root key. Unseal requires threshold M of N to be present and to prove possession of their share via challenge-response.

## Dual-mode operation


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


### Alternatives rejected

- **Returning shares in the initialization response.** Rejected outright: it
  puts every share in one place, in one process, at one moment — in a response
  body that will end up in a log or a terminal buffer. The SO distribution
  model exists to make that impossible.
- **Splitting the root key directly.** Rejected in favour of unseal-key
  indirection: rotating custody would otherwise mean rotating the root key,
  which would mean re-encrypting every byte of stored data.
- **RBAC-gating the claim endpoints.** Rejected because it cannot work:
  officers are ceremony-registry principals registered **before the user store
  exists**. The routes are identity-gated (an authenticated mTLS identity is
  required) and the handler enforces the real authorization — registry
  membership, certificate-thumbprint match, and nonce-signature verification.
- **Letting one administrator seal a Shamir barrier.** Rejected: `Seal` returns
  `ErrShamirBarrierRequiresQuorum`. If M officers are needed to open it, one
  person should not be able to close it either.
- **Persisting recovery shares server side.** Rejected: recovery shares are
  never returned in the generation response and **never persisted on the
  server**. They are an offline escrow tier by construction.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Shamir strategy, quorum accumulator, rekey, recovery, root token | `pkg/seal` | go-qrdb |
| Custodian records, enrolment, revocation | `pkg/custodian` | go-qrdb |
| HPKE key delivery | `pkg/keydelivery` | go-qrdb |
| Ceremony CA and officer certificate issuance | `pkg/ceremony` — see [../ceremony/](../ceremony/) | go-qrdb |
| PIN backend for SO/User PIN | `pkg/pin` — see [../auth-factors/](../auth-factors/) | go-qrdb |
| GF(256) Shamir primitive | — | **go-quicraft** |
| Hardware auth factors (PIV, FIDO2, PKCS#11) behind custodian methods | — | **go-xkms**, injected |

**go-qrdb owns the custody application layer; go-quicraft owns only the GF(256)
primitive.**

## Trust boundaries

- **No single custodian holds the complete root key.** Unseal requires M of N
  present, each proving possession of its share by challenge-response.
- **Shares never rest as raw garbage-collected strings.** A share awaiting
  delivery is held as a `secure.Secret` — go-enclave sealed ciphertext on the
  ordinary heap, opened into locked memory only for the instant a claim reads
  it. A share submitted toward an unseal quorum is held in a go-enclave
  locked-memory enclave (go-quicraft's `ProtectedShare`), which is not yet
  charged to the process locked-memory budget — see
  [../configuration/cli.md](../configuration/cli.md#locked-memory-outside-the-budget).
  Deduplication is done against a SHA-256 digest so the raw share is never
  retained as a map key; plaintext is materialized only transiently during
  reconstruction and wiped immediately after.
- **A share is delivered exactly once.** The server-side enclave is destroyed
  on successful claim.
- **The claim nonce has a 5-minute TTL** and must be signed with the officer's
  registered private key; the certificate thumbprint must match the enrolment
  record.
- **Custodian records are public metadata only** — `__sys:custodian/<id>`
  holds no share material. The encrypted share envelope is server-held and
  in-memory only during the enrolment transaction, then zeroed.
- **Rekey cryptographically revokes every prior share**, including already
  claimed ones.
- **Raw shares as request input are sanctioned in exactly one place**: root
  token generation, where officers submitting their claimed shares *is* the
  consent signal. No token is minted at initialization.

## What this feature deliberately does not do

- **No share recovery.** A lost share is replaced by rekey, not by re-issuing
  the same one.
- **No server-side recovery share storage.** Recovery keys are external escrow.
- **No share storage of any kind.** No API accepts share bytes for storage and
  no store holds them. The former `ShareService` (`SubmitShare`, `ListShares`,
  `GetShare`, `DeleteShare`, `GetShareCollectionStatus`), which persisted raw
  share bytes to the replicated store and served them back, has been removed
  along with the SDK's `Client.Shares()` / `transport.ShareClient`.
- **No hardware auth of its own.** PIV, FIDO2 and PKCS#11 custodian methods are
  go-xkms implementations reaching qrdb through the factor seam.
- **No quorum requirement on `none` or `passphrase` barriers.** The quorum seal
  gate applies to Shamir barriers only.

## Companion documents

- [seal/so-share-distribution-design.md](../seal/so-share-distribution-design.md)
  — the full design contract and invariants.
- [seal/custodians.md](../seal/custodians.md) — the custodian record schema,
  the ECDH + AES-256-GCM share-delivery envelope, nonce handling, replay
  prevention, zeroization guarantees and failure modes.
- [seal/so-dual-factor.md](../seal/so-dual-factor.md) — SO dual-factor
  authentication.
- [seal/shamir.md](../seal/shamir.md) — the original reference; this folder is
  its successor.
