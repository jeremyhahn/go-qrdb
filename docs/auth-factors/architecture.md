# Authentication Factors and PINs — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

The `Factor` framework, the user-side N-factor model, and PIN custody with
backend-native lockout.

## Problem

Authentication has to work with a software key on a laptop, a FIDO2 token, a
PIV card and a TPM — and the store cannot import any of the hardware SDKs. PINs
have their own problem: their protection is either a hash (software) or a
device's dictionary-attack counter (hardware), and those are not the same
guarantee.

## Shape chosen

Two seams, both filled by composition:

- **`authfactors.Factor` + `FactorRegistry`** — qrdb ships the framework and the
  **software mTLS reference factor**; FIDO2, PIV and PKCS#11 implementations
  come from go-xkms through `WithFactorRegistry`.
- **`pin.PINBackend`** — a **twelve-method** contract, with qrdb's
  `SoftwareBackend` and go-xkms's TPM2 / PKCS#11 backends behind it, injected
  through `WithPINBackend`.

### Two distinct factor surfaces

The SDK deliberately exposes two, and they are not the same thing:

| Accessor | Model | Used by |
|---|---|---|
| `Client.AuthFactors()` | ceremony-time custodian enrolment, challenge, verify, revoke | custodian unseal flows |
| `Client.UserAuthFactors()` | the user-side N-factor model: one User → many AuthFactors, with Add / List / Get / Remove / Rotate / Relabel / Login | ordinary user authentication |

`UserAuthFactors()` returns **nil** when the transport does not implement it —
the embedded transport currently exposes only the legacy `AuthFactorClient`
surface. Nil-check it.

### PIN strategies

| StrategyID | Owner | Lockout |
|---|---|---|
| `software` | qrdb (`pkg/pin/software_backend.go`) | **none** — hashing strength is the protection |
| `tpm2` | go-xkms | TPM 2.0 dictionary-attack protection |
| `pkcs11` | go-xkms | token-native policy via `C_Login` |

That table is the reason PINs are not credentials: lockout is backend-native and
`pkg/credentials` has no equivalent.

### Alternatives rejected

- **Verifying hardware factors in qrdb.** Rejected — the whole point of the
  registry is that qrdb never imports a hardware SDK. It ships the framework and
  one reference factor.
- **Interpreting the enrolment blob.** Rejected: qrdb stores
  `FactorEnrollment.Data` and never parses it, so a factor implementation cannot
  smuggle an authorization decision through it.
- **A software attempt-counter lockout.** Deliberately absent:
  `SoftwareBackend.GetLockoutStatus()` returns **nil** and `ResetLockout()` is a
  **no-op**. A counter that a process restart resets is theatre; a caller that
  needs real lockout injects a hardware backend. This is stated rather than
  implied so nobody assumes protection that is not there.
- **Choosing the hash algorithm from the runtime.** Rejected: the algorithm is
  persisted **inside each `PINRecord`** and resolved at verification time. That
  is what lets a FIPS binary verify a PIN hashed by a non-FIPS binary and vice
  versa.

## Hashing

| Factory | Algorithm |
|---|---|
| `DefaultHashConfig()` | Argon2id — 3 passes, 64 MiB, 4 threads, 32-byte key, 16-byte salt |
| `FIPSHashConfig()` | PBKDF2-SHA256, **600 000 iterations**, NIST SP 800-132 |
| `AutoDetectHashConfig()` | calls `crypto/fips140.Enabled()` and picks between them |

Supported PBKDF2 hashes: `sha256`, `sha384`, `sha512`, `sha512_256`.

## CTAP2 interop

`SoftwareBackend` caches the CTAP2 `SHA-256(PIN)[:16]` hash in an atomic
pointer, so a virtual authenticator can answer CTAP2 PIN verifications without a
separate set call after a process restart. It is wiped on `Close`. Backends that
can verify a CTAP2 hash implement the optional `FIDO2HashVerifier` interface.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| `Factor` framework, `FactorRegistry`, software mTLS factor | `pkg/authfactors` | go-qrdb |
| PIN contract, `Service`, `SoftwareBackend`, hashing | `pkg/pin` | go-qrdb |
| User-side N-factor enrolment records | `pkg/user` — see [../identity-rbac/](../identity-rbac/) | go-qrdb |
| FIDO2, PIV, PKCS#11 factors; TPM2 and PKCS#11 PIN backends | — | **go-xkms**, injected |
| Hardware verification and UX | — | **go-xkms** / `xkeyctl` / `platformctl` |

## Trust boundaries

- **qrdb does not perform hardware verification.** The CLI's `auth login`
  resolves a `(method, lookup key)` tuple to a user; it does not touch a device.
  End-to-end hardware login is `xkmsctl auth login` or `platformctl auth login`.
- **Minimum PIN length is 6 characters**, enforced in `Service` before
  delegation, so every backend gets the same floor.
- **Verification is constant-time** (`crypto/subtle`) and hashes are re-derived
  on every call.
- **The software backend has no lockout.** Its protection is the hash.

## What this feature deliberately does not do

- **No hardware factor implementations.**
- **No hardware verification in the qrdb CLI.**
- **No software lockout counter.**
- **No interpretation of enrolment data.**
