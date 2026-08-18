# Initialization Ceremony — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

The ceremony is the barrier's custody bootstrap: it establishes the CA, splits
the secret among Security Officers, and holds each share until its officer
claims it. It is **not** the admin bootstrap — that is
[../firstboot/](../firstboot/).

## Problem

A fresh system has no CA, no officers, no shares and no admin. Getting from
nothing to a running M-of-N custody arrangement is a sequence with an order that
matters, and doing it wrong — minting shares before officers exist, or handing
every share out at once — defeats the point of the arrangement.

## Shape chosen

A three-state machine, driven by claims:

```
awaiting_init  ──Initialize()──>  enrolling  ──allClaimsComplete()──>  operational
```

- **awaiting_init**: System is freshly deployed, no ceremony has been performed.
- **enrolling**: Ceremony in progress. Officers claim certificates and Shamir shares.
- **operational**: All officers have claimed their artifacts. System is ready.

In **single-admin mode** (`threshold < 2`) the state transitions directly from `awaiting_init` to `operational`. In **M-of-N mode** (`threshold >= 2`) the daemon stays in `enrolling` until every custodian has claimed.

In **single-admin mode** (`threshold < 2`) the state goes straight from
`awaiting_init` to `operational`. In **M-of-N mode** (`threshold >= 2`) the
daemon stays in `enrolling` until every custodian has claimed.

## PIN handling — never raw bytes

The ceremony never stores raw PIN bytes inside the credential store any more. PIN material is owned by a `pin.PINBackend` (see `pkg/pin/backend.go:33-84` and [../pin/README.md](../auth-factors/architecture.md)) injected through the qrdb server option:

```go
// pkg/server/server.go:488-495
func WithPINBackend(backend pin.PINBackend) ServerOption { ... }
```

At bootstrap the ceremony does the following with each PIN it receives from the caller:

1. `pin.Service.SetSOPIN(currentSOPIN, newSOPIN)` — hashes (Argon2id / PBKDF2 depending on FIPS mode) and persists through the backend.
2. `pin.Service.SetUserPIN(soPIN, newUserPIN)` — same, gated by SO verification.
3. The raw byte buffers used to receive the PIN material are wiped (`secure.Wipe`) before the bootstrap call returns.

For an M-of-N initialisation the User PIN is **also** the Shamir secret. The User PIN bytes are split via `shamir.Split` and the original bytes are wiped from local memory once the shares are produced; the PIN itself is never persisted in plaintext anywhere.

When no `WithPINBackend` is supplied the daemon constructs a default `pin.SoftwareBackend` (Argon2id / PBKDF2 auto-selected by `crypto/fips140.Enabled()`) backed by the barrier-protected storage. Hardware deployments inject TPM2 / PKCS#11 backends from go-xkms through the same option without ceremony needing to know which backend it is talking to.

> **Migration note**: The legacy `credentialSOPin` / `credentialUserPin` storage keys remain readable for in-place upgrades from older deployments. New writes go exclusively through the `pin.PINBackend`; the legacy keys are wiped on first successful PIN backend write.

## Share lifecycle

Shares are **never persisted to disk.**

Shares are **never persisted to disk**. During M-of-N enrollment:

1. `Initialize()` calls `shamir.Split()` on the user PIN
2. Each share is stored in a `memguard.LockedBuffer` (mlock'd, guard-paged memory)
3. Officer connects via mTLS, calls `BeginClaimCert()` to get a challenge nonce
4. Officer signs nonce with CSR private key, calls `CompleteClaimCert()` to receive certificate
5. Officer calls `ClaimShare()` to retrieve their share bytes
6. The `LockedBuffer` is `Destroy()`'d (secure wipe) immediately upon claim
7. When all claims complete, the state transitions to `operational`

If the ceremony is abandoned (e.g., server restart), all shares are lost and the ceremony must be re-run. This is intentional: shares are secrets that belong to custodians, not the system.

**Invariants, pinned by tests in `pkg/ceremony/share_store_test.go`:**

- `Store` **consumes** the caller's slice, wiping it. A copy left on the heap would survive in a core dump or a swapped page, defeating the enclave the share was just placed in.
- `Claim` **destroys** the enclave and retains only the fact that a claim occurred, so a second attempt is refused rather than served. Once a custodian retrieves their share, they alone hold it.
- What persists is the officer **record** (`ceremony/officers/<name>`): certificate thumbprint or public key, share **index**, auth factor, timestamps. **Never share bytes.**


Recovering the root key later therefore requires a threshold of holders to
bring their shares back. **The server cannot do it alone**, which is exactly
the property an M-of-N ceremony is chosen for.

### Officer enrollment kinds

An officer supplies key material one of three ways, parsed by `ceremony.ParseEnrollmentSpec` (`<username>:<kind>:<path>`, the same vocabulary the platform admin uses):

| Kind | Entry point | Trust |
|------|-------------|-------|
| `csr` | `RegisterOfficer` | Derived — the ceremony CA signs it |
| `cert` | `EnrollExternalOfficer` | Verified — parsed, in-validity, chains to the configured external CA pool |
| `pubkey` | `EnrollExternalOfficer` | Asserted by the administrator running the ceremony; a bare key has no chain |

The two-field form `<username>:<path>` means `csr`.


## The CA seam

The ceremony requires a narrow CA interface that decouples it from any specific CA implementation:

```go
type CeremonyCA interface {
    SignCSR(csrPEM []byte, opts *SignCSROptions) (*x509.Certificate, error)
    CACertificate() (*x509.Certificate, error)
    CABundle() ([]byte, error)
    IssueCertificate(req *IssueCertRequest) (*IssuedCert, error)
}
```

Both qrdb's `cert.CertProvider` and xkms's `ca.TrustedCA` can satisfy this via thin adapters.

## Threshold initializers

The `ThresholdInitializer` interface supports hardware-backed threshold ceremonies (e.g., HSM-native threshold). Implementations are registered via `ThresholdRegistry` and ship in go-xkms. qrdb itself ships only a `NoopThresholdInitializer` (`pkg/ceremony/threshold_noop.go`) for environments without hardware support.


## Authenticator types

The ceremony is **authenticator-agnostic at the cryptographic layer** — it only
verifies signatures over challenge nonces. Officer credentials may therefore be
backed by anything producing a `crypto.Signer`. The kind is carried as metadata
so higher layers can route signing to the right backend.

| Constant | Value | `AuthenticatorRef` |
|---|---|---|
| `AuthenticatorPIV` | `piv` | PIV slot identifier (e.g. `9a`), NIST SP 800-73 |
| `AuthenticatorFIDO2` | `fido2` | base64url-encoded credential id, CTAP2 over USB-HID/NFC |
| `AuthenticatorSoftware` | `software` | unused |

The empty string is valid and maps to `AuthenticatorSoftware` via
`Normalized()`, so callers that never set the field keep working. Anything else
gets `ErrInvalidAuthenticatorType`.

**The metadata is not used during verification.** Both `BeginClaimCert` and
`CompleteClaimCert` operate on the officer's CSR public key regardless of which
authenticator produced the key. Concrete FIDO2 / PIV / PKCS#11 implementations
live in go-xkms and are injected through `authfactors.FactorRegistry`; qrdb
ships the `Factor` framework and the software-mTLS reference factor.

## Relationship to firstboot

`ceremony.Initialize` is **barrier-only**. It is **NOT** the canonical admin
bootstrap.

| Concern | Package | Function |
|---------|---------|----------|
| Barrier custody (M-of-N split, custodian enrolment, Shamir share lifecycle) | `pkg/ceremony` | `ceremony.Initialize` |
| System tenant + built-in roles + platform admin user + admin client cert | `pkg/firstboot` | `firstboot.Initialize` |

Daemon startup runs them in order:


Daemon startup runs them in order: ceremony (PINs → barrier → CA cert, SO cert,
shares) then firstboot (system tenant → built-in roles → admin user → admin
client cert). **Both are idempotent and safe to re-run on cluster nodes** —
leader writes propagate via Raft and follower runs are no-ops.

### Alternatives rejected

- **Persisting shares server side.** Rejected: shares are secrets that belong to
  custodians, not the system. If the ceremony is abandoned — a server restart
  mid-enrolment — all shares are lost and it must be re-run. That is
  intentional.
- **Storing raw PIN bytes in the credential store.** Rejected: PIN material is
  owned by a `pin.PINBackend`, hashed with Argon2id or PBKDF2 depending on FIPS
  mode. Raw receive buffers are wiped (`secure.Wipe`) before the bootstrap call
  returns. In M-of-N the User PIN *is* the Shamir secret and its bytes are wiped
  once the shares are produced.
- **A single monolithic bootstrap.** Rejected: barrier custody and admin
  identity have different trust models and different failure modes. Splitting
  them is what lets an operator run one without the other.
- **Serving a claimed share twice.** Rejected — `Claim` destroys the enclave and
  retains only the fact that a claim occurred, so a second attempt is refused.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Ceremony state machine, officers, share store | `pkg/ceremony` | go-qrdb |
| PIN backend contract and software implementation | `pkg/pin` — see [../auth-factors/](../auth-factors/) | go-qrdb |
| Custodian records and protocol | `pkg/custodian` — see [../shamir-custody/](../shamir-custody/) | go-qrdb |
| Barrier initialization | `pkg/seal` — see [../barrier-seal/](../barrier-seal/) | go-qrdb |
| Admin/tenant/role bootstrap | `pkg/firstboot` — see [../firstboot/](../firstboot/) | go-qrdb |
| Hardware threshold initializers, FIDO2/PIV/PKCS#11 factors | — | **go-xkms**, injected |
| Shamir GF(256) primitive | — | **go-quicraft** |

`CeremonyCA` is deliberately narrow so it decouples the ceremony from any
specific CA. **Both qrdb's `cert.CertProvider` and xkms's `ca.TrustedCA` satisfy
it through thin adapters.**

## Trust boundaries

- **Shares live only in memguard enclaves**, mlock'd and guard-paged, destroyed
  on claim.
- **`Store` consumes the caller's slice, wiping it** — a copy left on the heap
  would survive in a core dump or a swapped page.
- **What persists is the officer record** (`ceremony/officers/<name>`):
  certificate thumbprint or public key, share **index**, auth factor,
  timestamps. **Never share bytes.**
- **Claim is a nonce challenge/response over mTLS**, matched against the
  officer's enrolment record.
- Custodian metadata is Raft-replicated at `__sys:custodian/<id>`; share
  ciphertext is held by the custodian, not the server.

## What this feature deliberately does not do

- **No admin, tenant or role creation.** That is firstboot.
- **No share recovery.** An abandoned ceremony loses its shares by design.
- **No hardware threshold ceremony of its own** — qrdb ships only
  `NoopThresholdInitializer`; real implementations come from go-xkms through
  `ThresholdRegistry`.
- **No `qrdb ceremony init` verb.** It was removed; the root `qrdb init` is the
  entry point.
