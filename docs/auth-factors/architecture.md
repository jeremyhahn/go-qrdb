# Authentication Factors and PINs — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

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
| `Client.UserAuthFactors()` | the user-side N-factor model: one User → many AuthFactors, with Add / List / Get / Remove / Delete / Rotate / Relabel / Login | ordinary user authentication |

`UserAuthFactors()` returns **nil** when the transport does not implement it.
Every transport, including embedded, implements `transport.UserAuthFactorClient`
alongside the ceremony-time `AuthFactorClient` surface; nil-check it anyway —
the two surfaces are independent capabilities, and a future transport is not
guaranteed to carry both.

### PIN strategies

| StrategyID | Owner | Lockout |
|---|---|---|
| `software` | qrdb (`pkg/pin/software_backend.go`) | **none in the backend** — hashing strength is the protection; the node fronts every backend with the SO PIN lockout decorator (`pin.NewLockoutBackend`) |
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

### Conditional add: one guarded write decides it

A consumer that hands out a one-time claim — "this user may enrol their first
PIV card, once" — needs the check and the add to be one step. Two concurrent
adds that each read "no PIV card yet" and each write one is the defect, and no
amount of checking before the write closes that window.

So every user's factors carry one more record: the **factor set**, at
`__sys:auth/factorsets/{userID}` in the same routed store as the factors
themselves, holding a version and the user's active factor ids by type. An
`Add` that carries a condition — *no active factor of this type*, or *the set
is at version N* — evaluates it against that record and commits the new set
with **one guarded write**: `PutIfAbsent` for the first, `CompareAndSwap`
against the bytes it read for every later one. Two adds that read the same
version cannot both swap; the loser re-reads, finds the condition false, and
is refused with `auth.FactorConditionFailedError`. The decision is the write's,
so it holds wherever the write is linearizable: on one node, and through Raft
on many, with no opcode the cluster does not already apply.

Every membership change — `Add`, `Remove`, `Delete`, `Rotate` — commits the
set the same way, so the version a caller read from `List` moves whenever anything a
condition could depend on moves. An unconditional add commits it too; it only
reports no version back.

The record is a cache of the user's factors, not their source of truth. Each
commit re-derives the active set from the primary records it names plus the
user index, so a process that stopped between the set commit and the index
writes leaves nothing a later commit does not repair. The new factor's primary
record is written before the set commits and released with `CompareAndDelete`
when the condition refuses it, so a refused add leaves nothing behind.

A store with no guarded writes cannot make this decision and does not pretend
to: a conditional add on it is refused with
`service.ErrUserAuthFactorAtomicStoreRequired`, and `List` reports version 0.

### Revoke and delete: two ways a factor ends

A factor's credential and SubjectPublicKeyInfo are each claimed by an index
record naming the factor, and an add that finds a claim is refused. `Remove`
is a **revocation**: the record is stamped and every claim stays, so a
credential an administrator withdrew can never be bound again — not by the
same user, not by anyone in the tenant.

That is wrong for the other way a factor ends. A caller that adds a factor
and then has to roll back — the enrolment it was part of failed — would
leave the credential claimed by a factor that never served, and the retry
would be refused for colliding with its own abandoned attempt. `Delete` ends
the factor without a trace: the primary record, the user index entry, every
claim naming it and the factor-set update go in **one** multi-key guarded
batch (`kvstore.MultiGuardedBatchKVStore`, one Raft proposal on a cluster).
Each key the batch writes is guarded on the bytes the delete read, so a
concurrent change to any of them aborts the batch and the delete re-plans;
nothing is ever half-removed. A claim that names a different factor is
guarded and left alone.

Delete **refuses a revoked factor**. If it did not, delete-then-add would be a
way to undo any revocation, and the claims a revocation keeps are the whole of
what makes it permanent. The two verbs therefore partition the factor's life:
`Delete` before revocation, nothing after it.

A store that cannot commit several guarded keys at once cannot make the delete
atomic and does not try: `Delete` on it is refused with
`service.ErrUserAuthFactorBatchStoreRequired`. Every production store — the
plaintext engine store and the tenant barrier's encrypted store — implements
the batch.

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
- **A new Raft opcode for the conditional add.** Rejected. Every node would
  need the opcode before any could propose it, which is a cluster feature gate
  for a decision the existing guarded writes already make linearizably.
- **Deciding the condition from a `List` before the `Add`.** Rejected — that
  is the race the feature exists to close. A caller may still `List` to learn
  the version; the server decides against the record, at the write.
- **A lock record around the add.** Rejected. A lock needs a holder, a lease
  and a recovery path for a holder that dies, all to serialize one write that
  `CompareAndSwap` already serializes, and it would stall every other change
  to the user's factors while held.
- **Freeing the claims on `Remove`.** Rejected: a revocation that releases
  the credential is a revocation the next add can reverse. Revocation keeps
  its claims; only the separate `Delete`, refused once revoked, frees them.
- **Deleting with sequential writes.** Rejected: a process that stopped
  between the record's delete and its index deletes would leave the
  credential claimed by a factor that no longer exists, which is the defect
  `Delete` is for, with no record left to retry from.
- **Verifying hardware factors in qrdb.** Rejected — the whole point of the
  registry is that qrdb never imports a hardware SDK. It ships the framework and
  one reference factor.
- **A lockout inside the software backend.** Deliberately absent:
  `SoftwareBackend.GetLockoutStatus()` returns **nil** and `ResetLockout()` is a
  **no-op**. The SO PIN lockout is a separate decorator,
  `pin.NewLockoutBackend`, that a node installs around whichever backend it
  uses, so it covers every SO PIN check whatever the backend, with separate
  counters for anonymous sealed-window checks and authenticated ones. Its
  counters are in memory and a process restart clears them; a deployment that
  needs a lockout that survives restarts injects a hardware backend, which the
  decorator then fronts.
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
- **The software backend has no lockout of its own.** Its protection is the
  hash. A node fronts it with the SO PIN lockout decorator
  (`pin.NewLockoutBackend`): 5 free failures, then a backoff doubling from 1 s
  to 15 min, in memory.

## The seam's error contract

Because the `Factor` implementations live outside this repository, the errors
they return are part of the seam and every one of them has an `sdk/go` surface:

| Condition | SDK name |
|---|---|
| Enrolment input invalid | `AuthFactorEnrollmentInvalidError` |
| Challenge expired | `AuthFactorChallengeExpiredError` |
| Signature did not verify | `AuthFactorVerificationFailedError` |
| Caller named a factor other than the enrollment's | `AuthFactorMismatchError` |
| Device unreachable | `AuthFactorHardwareUnavailableError`, or the sentinel `ErrAuthFactorHardwareUnavailable` |

Both transports map a hardware-unavailable condition to 503; the SDK alias is
what lets an out-of-repo factor construct the error that arm waits for.

An error on this seam that has no SDK alias is a handler arm that can never
fire. When a condition is added to the contract, export it here in the same
change.

## What this feature deliberately does not do

- **No hardware factor implementations.**
- **No hardware verification in the qrdb CLI.**
- **No lockout counter inside the software backend** (the SO PIN lockout is
  the separate decorator described above).
