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

`UserAuthFactors()` returns **nil** when the transport does not implement it.
Every transport, including embedded, implements `transport.UserAuthFactorClient`
alongside the ceremony-time `AuthFactorClient` surface; nil-check it anyway —
the two surfaces are independent capabilities, and a future transport is not
guaranteed to carry both.

### PIN strategies

| StrategyID | Owner | Lockout |
|---|---|---|
| `software` | qrdb (`pkg/pin/software_backend.go`) | **none** — hashing strength is the protection |
| `tpm2` | go-xkms | TPM 2.0 dictionary-attack protection |
| `pkcs11` | go-xkms | token-native policy via `C_Login` |

That table is the reason PINs are not credentials: lockout is backend-native and
`pkg/credentials` has no equivalent.

### Credentials are separated by the STORE, not by the key

A `(method, lookup key)` tuple is not unique across a deployment: two
organizations can hold the same certificate, or enrol the same token. The
lookup index is keyed `__sys:auth/lookup/{type}/{credKeyHex}` and carries no
tenant segment, so what decides whether two tenants collide is which STORE the
key is looked up in.

The user-side auth-factor records are therefore routed through
`tenant.StoreResolver`, the same resolver the user and role stores route
through, and for the same reason: they are the same class of data — per-tenant
IAM. A prefix tenant gets the shared store confined to its identity namespace;
a dedicated-isolation tenant gets a store backed by its own barrier, so the
record holding the credential material is encrypted with that tenant's DEK.

Enrolment routes on the tenant that owns the USER, not on the enrolling
context, so a record lands where the login that follows will look for it. A
caller that may not reach the owning tenant is refused rather than redirected
into its own — see [engineering.md](engineering.md#which-id-a-call-routes-on).

**The ceremony-time custodian surface (`Client.AuthFactors()`) is routed the
same way.** Its records are per-tenant too — a custodian is
enrolled for a specific tenant's barrier — but there is no user-id-shaped
"owner" to look up: a subject is a caller-supplied label and an enrollment id
is issued inside the enrolling tenant and only ever presented back to it. So
every call — `EnrollFactor`, `ChallengeFactor`, `VerifyFactor`, `ListFactors`,
`RevokeFactor` — routes on the **context** tenant, the same rule the user-side
surface applies to its own by-id verbs. See
[engineering.md](engineering.md#the-custodian-surface-is-routed-too).

### Alternatives rejected

- **Putting the tenant in the lookup key** (`.../lookup/{tenant}/{type}/...`).
  Rejected. It is the smaller change and the wrong shape: this repository has
  exactly one mechanism for per-tenant IAM data, and a tenant segment in this
  one key would inaugurate a second, divergent one for the same class of data.
  Every later reader would have to know which surface uses which, and that
  divergence is where the next isolation defect gets written. Routing also has
  the better migration: records MOVE rather than being rewritten in place,
  which is the safer operation to interrupt.
- **Checking the tenant after resolving the factor** — resolve the credential
  in one flat keyspace, then compare the factor's owner's tenant against the
  session's. Rejected. It leaves one record per credential, so the second
  tenant to enrol a credential is refused for a collision with a record it
  cannot see, and told the factor id that caused it. A boundary enforced after
  the read is also one any new caller of the store can skip.
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
| `AutoDetectHashConfig()` | calls `seal.FIPSEnabled()` and picks between them |

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
- **A credential resolves only inside the tenant that holds it.** The store a
  lookup reaches is selected from the tenant the login was begun under, and
  nothing downstream re-checks it — so every path to the factor records goes
  through the routed service, and a composition that cannot resolve a tenant
  is refused the surface rather than served an unrouted one.
- **A caller cannot supply an attestation claim.** Factor metadata keys under
  `attest_` are server-owned and refused on `Add` and `Rotate`. Enforced once
  in the user auth-factor **service**, not per handler, so REST, gRPC, MCP, the
  native QUIC transport, the Unix socket and the in-process embedded client all
  inherit it — and a transport added later inherits it by construction rather
  than by someone remembering. This matters because qrdb is the *store* for a
  claim another system's login policy trusts: qrdb performs no hardware
  verification itself, so the only thing protecting the claim is that no wire
  path can write it.
- **The server writes the attestation namespace; a trusted host says what
  was proved.** Every `Add` and `Rotate` stamps `attest_level=none` — the
  factor proved nothing, and the record says so rather than staying silent.
  A ceremony-verified attestation is recorded only through
  `service.AttestationRecorder` (`AddAttested` / `RotateAttested`, taking a
  typed `auth.VerifiedAttestation`), a service-layer interface no transport
  handler maps to. The boundary is in-process versus wire, not a permission:
  the process hosting the service is the one that verified the chain, and
  nothing arriving over any listener can construct the typed value.
- **Minimum PIN length is 6 characters**, enforced in `Service` before
  delegation, so every backend gets the same floor.
- **Verification is constant-time** (`crypto/subtle`) and hashes are re-derived
  on every call.
- **The software backend has no lockout.** Its protection is the hash.

## The seam's error contract

Because the `Factor` implementations live outside this repository, the errors
they return are part of the seam and every one of them has an `sdk/go` surface:

| Condition | SDK name |
|---|---|
| Enrolment input invalid | `AuthFactorEnrollmentInvalidError` |
| Challenge expired | `AuthFactorChallengeExpiredError` |
| Signature did not verify | `AuthFactorVerificationFailedError` |
| Device unreachable | `AuthFactorHardwareUnavailableError`, or the sentinel `ErrAuthFactorHardwareUnavailable` |

The last row is the reason this table exists. Both transports map a
hardware-unavailable condition to 503, but the type and its sentinel were the
only members of the contract with no SDK surface, so no out-of-repo factor
could construct the error the handler was waiting for and the arm was
unreachable in a deployed system rather than merely unused here.

An error on this seam that has no SDK alias is a handler arm that can never
fire. When a condition is added to the contract, export it here in the same
change.

## What this feature deliberately does not do

- **No hardware factor implementations.**
- **No hardware verification in the qrdb CLI.**
- **No software lockout counter.**
- **No interpretation of enrolment data.**
