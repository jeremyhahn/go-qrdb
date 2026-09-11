# Authentication Factors and PINs — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Exported surface

`sdk/go`:

```go
func (c *Client) AuthFactors()     transport.AuthFactorClient      // ceremony-time custodian surface
func (c *Client) UserAuthFactors() transport.UserAuthFactorClient  // user-side N-factor; NIL if unsupported
func (c *Client) Factors()         transport.FactorClient          // nil if unsupported
func (c *Client) Login()           transport.LoginClient           // nil if unsupported
func (c *Client) Sessions()        transport.SessionClient         // nil if unsupported
```

Composition-plane options (Tier 2):

```go
func WithFactorRegistry(reg authfactors.FactorRegistry) ServerOption
func WithPINBackend(backend pin.PINBackend) ServerOption
```

### `PINBackend` — the canonical contract

```go
type PINBackend interface {
    Strategy() StrategyID

    SetSOPIN(currentSOPIN, newSOPIN string) error
    SetUserPIN(soPIN, newUserPIN string) error

    ChangeSOPIN(currentSOPIN, newSOPIN string) error
    ChangeUserPIN(currentUserPIN, newUserPIN string) error

    VerifySOPIN(pin string) error
    VerifyUserPIN(pin string) error

    IsInitialized() bool
    SOPINSet() bool
    UserPINSet() bool

    GetLockoutStatus() *LockoutStatus
    ResetLockout(soPIN string) error
}
```

Twelve methods, no more. Backends fall into two categories:

**Twelve methods, no more.** Backends fall into two categories:

- **Software** (`pkg/pin.SoftwareBackend`) — Argon2id or PBKDF2 hashing,
  persistence via the narrow `Storage` interface, **no native lockout** (the
  hash is the protection).
- **Hardware** (go-xkms) — TPM2 uses PlatformSRK password auth with native
  dictionary-attack protection; PKCS#11 uses token-native `C_Login` /
  `C_SetPIN` with the HSM's lockout policy.

```go
func NewSoftwareBackend(store Storage, hashConfig HashConfig) (*SoftwareBackend, error)

func DefaultHashConfig()    HashConfig  // Argon2id: 3 passes, 64 MiB, 4 threads, 32-byte key, 16-byte salt
func FIPSHashConfig()       HashConfig  // PBKDF2-SHA256, 600 000 iterations, NIST SP 800-132
func AutoDetectHashConfig() HashConfig  // seal.FIPSEnabled() decides

const FIDO2PINHashSize = 16 // CTAP2 mandate: SHA-256(PIN)[:16]
func ComputeFIDO2PINHash(pin string) []byte
func VerifyFIDO2PINHash(expected, actual []byte) bool

type FIDO2HashVerifier interface{ /* optional PINBackend capability */ }
func (s *Service) VerifyFIDO2Hash(hash []byte) bool
```

> **Correction carried forward.** The retired `docs/pin/README.md` spelled this
> constant `FIPO2PINHashSize`. The real identifier is **`FIDO2PINHashSize`**
> (`pkg/pin/service.go`); the older spelling was a typo, and anything copied
> from that document should be re-checked against the code.

`PINRecord` (`pkg/pin/hash.go`):

```go
type PINRecord struct {
	Algorithm string          `json:"algorithm"` // "argon2id" or "pbkdf2"
	Hash      []byte          `json:"hash"`
	Salt      []byte          `json:"salt"`
	Params    json.RawMessage `json:"params,omitempty"`
}
```

Storage keys: `so-pin` and `user-pin`, each a JSON-encoded `PINRecord`.

## Invariants

- **Minimum PIN length is 6 characters**, enforced in `Service` **before**
  delegation, so every backend inherits the same floor.
- **The hash algorithm is persisted in the record and resolved at verification
  time**, not read from the runtime. A FIPS binary can verify a PIN hashed by a
  non-FIPS binary and vice versa.
- **Verification re-derives the hash on every call** and compares in constant
  time (`crypto/subtle`).
- **`SoftwareBackend.GetLockoutStatus()` returns nil and `ResetLockout()` is a
  no-op.** There is no software attempt counter. A caller that needs lockout
  injects a hardware backend. Do not read the nil as "not locked out" in a
  security decision.
- **`SoftwareBackend` loads existing records at construction**, so
  `SOPINSet()` / `UserPINSet()` report prior state correctly after a restart.
- **The CTAP2 hash is cached in an atomic pointer** and wiped on `Close`, so a
  virtual authenticator answers CTAP2 verifications after a restart without a
  separate set call.
- **Concurrent reads on `SoftwareBackend` use atomic pointers, no locks.**
- **Passing `nil` for `Storage` gives in-memory-only operation** — for tests.
- **`Client.UserAuthFactors()` returns nil** on a transport without the
  interface (the embedded transport exposes only the legacy `AuthFactorClient`).
  `Factors()`, `Login()` and `Sessions()` likewise. **Nil-check them** — a nil
  is not an error value.
- **Factor metadata keys under `auth.ReservedMetadataPrefix` (`attest_`) are
  server-owned and refused on `Add` and `Rotate`.** Enforced by
  `auth.ValidateMetadata`, called from the user auth-factor **service** so
  every transport inherits one rule rather than each re-implementing it.
  Refused, never stripped — a caller who set the key and got a success would
  believe it was recorded. Matching is case-insensitive and is a **prefix**, so
  an attestation field invented later is covered without an edit; the literal
  boundary means `attestation_root` is not reserved. See
  [architecture.md](architecture.md#trust-boundaries) for why this is a trust
  boundary and not a validation nicety.
- **The server stamps `attest_level=none` on every `Add` and `Rotate`**
  (`auth.Unattested().Stamp`), so a factor that proved nothing is positively
  marked. **A verified attestation is recorded through
  `service.AttestationRecorder`** — assert the servicer to it — with a typed
  `auth.VerifiedAttestation{Level, AAGUID}`; `Level` must be one of
  `auth.AttestationLevelNone` / `Software` / `Hardware` or the call fails with
  `ErrInvalidAttestation` before any write. The recorder is deliberately not
  on `UserAuthFactorServicer` and not on any transport: a transport client
  cannot be asserted to it (pinned by a test in `sdk/go/transport/embedded`).
  Read the keys back by name: `auth.MetadataAttestLevel`,
  `auth.MetadataAttestAAGUID`. All of it is re-exported by `sdk/go`.
- **qrdb never interprets `FactorEnrollment.Data`.**
- **The qrdb CLI performs no hardware verification.** `auth login` resolves a
  `(method, lookup key)` tuple to a user through the `LoginAuthFactor` RPC.

## Factor types and lookup keys

| Method | Lookup key |
|---|---|
| `mtls` | SHA-256 of the certificate DER |
| `fido2` | WebAuthn `credentialID` bytes |
| `piv` | SHA-256 of the slot certificate DER |

**A lookup key is not unique across the deployment, and is not meant to be.**
Two organizations can hold the same certificate or enrol the same token, and
the credential itself says nothing about which of them is presenting it. The
tenant that separates them is the STORE, never the key — see the next section.

## Tenant scoping — the store, not the key

The user-side auth-factor records are per-tenant IAM, and they are routed
exactly as users and roles are: through `tenant.StoreResolver`.

```go
func NewUserAuthFactorService(cfg UserAuthFactorServiceConfig) (UserAuthFactorServicer, error)               // ONE tenant's store
func NewRoutingUserAuthFactorService(cfg RoutingUserAuthFactorServiceConfig) (UserAuthFactorServicer, error) // tenant-routed
func MigrateFactorsToTenants(ctx, shared kvstore.KVStore, resolver, owners, logger) (*FactorMigrationReport, error)
```

- **`UserAuthFactorServiceConfig.KV` is an `AuthFactorKV`** — `Put`, `Get`,
  `List` — satisfied verbatim by both `service.KVServicer` and
  `kvstore.KVStore`, which is what lets the router hand a resolved per-tenant
  store straight to the per-tenant service with no adapter between them. A
  missing key reads back as a typed error from one and as `(nil, nil)` from the
  other; every read treats "error or empty" as absent, so neither convention is
  privileged.
- **The keys are unchanged** — `__sys:auth/factors/{id}`,
  `__sys:auth/users/{userID}/factors/{id}`,
  `__sys:auth/lookup/{type}/{credKeyHex}` — and carry no tenant segment. The
  physical key is `{tenant}/` + that, supplied by the resolver's confinement,
  and `{tenant}/` is `tenant.IdentityNamespace`, the same prefix the routed
  user and role stores write under.
- **Composition roots build the ROUTED service, always.** Where no resolver
  exists the surface is withheld rather than served flat: `pkg/server`'s
  `Start()` constructs nothing without `WithTenantStoreResolver`, and the
  embedded node refuses to wire the login ceremony with `ErrNoTenantRouting`.
  There is no unrouted fallback, because an unrouted fallback is the defect.

### Which id a call routes on

| Call | Routes on | Rationale |
|---|---|---|
| `Add`, `AddAttested` | the tenant that **owns the user id**, via `tenant.IdentityIndex`; a caller that may not reach it is refused with `*tenant.CrossTenantDeniedError` | an enrolment must land where the user lives, or the login that follows — begun under the user's tenant — finds nothing and reports a bad credential |
| `List`, `HasActiveFactor` | the tenant that owns the user id, via `tenant.OwningTenant`; an unreachable or unindexed id routes to the caller's own tenant and finds nothing | the shared by-id read rule; it discloses nothing about where the id lives |
| `Get`, `Remove`, `Rotate`, `Relabel`, `MarkUsed` | the **context** tenant | a factor id is issued inside a tenant and only travels back to it; there is no index for it and none is wanted |
| `LookupByCredentialKey` | the **context** tenant — the tenant the login was begun under | this is the isolation boundary; nothing downstream re-checks it |

The write path is stricter than the read path on purpose. A write routed to
the caller's own tenant on a denial would bind a credential of the caller's
choosing to another tenant's user id, leaving the boundary resting on the
downstream user lookup refusing the same cross-tenant read — two components
having to agree. The refusal holds it where the write is.

### `LastUsedAt` is detached with `context.WithoutCancel`

`LoginResolver.Resolve` updates `LastUsedAt` in a goroutine so a returning
login response cannot cancel it. The detached context is derived with
`context.WithoutCancel`, **not** `context.Background()`: the store is
tenant-routed and reads the tenant off that context, so a background context
would resolve the system tenant, look in the wrong store, and silently stop
advancing `LastUsedAt` for every other tenant. Nothing would fail; the value
would simply stop moving.

### Migrating records written before routing

Deployments written by an earlier build hold their factor records at the flat
root, with no tenant anywhere in the key or in the record.
`service.MigrateFactorsToTenants` moves them, and the routed service runs it
itself: hand it the legacy store as `RoutingUserAuthFactorServiceConfig.LegacyStore`
and it migrates ONCE, before it answers anything.

- **It runs at first use, not at construction.** The records are read through
  Raft, and a composition root builds its services while the shard is still
  electing — a linearizable read there fails with "not leader, leader unknown"
  on a node that is otherwise healthy. First use is the earliest moment the
  store can answer.
- **No operation can outrun it**, which is why it lives in the service rather
  than in a second call a root makes. Serving before migrating would strand
  every already-enrolled credential for as long as the gap lasted, and that
  failure looks like a bad credential rather than a migration that has not run.
- **What consumes the one-shot depends on the outcome.** A STORE failure does
  not, and is returned: the next call tries again. A TERMINAL refusal does, and
  is not returned: re-running would only re-refuse. A TRANSIENT refusal does
  **not**, and is not returned either: the service serves every other tenant
  normally and the next call re-runs over what is left, which by then is the
  refused records alone. Latching on a transient refusal would make the
  resumability above false — a barrier sealed for ten minutes would strand its
  records for the life of the process.

- **The tenant is established from the owning USER**, through
  `tenant.IdentityIndex`. Nothing in the key or the record supplies it: an
  `AuthFactor` has a `UserID` and no tenant, so unlike an index name — which
  carries the namespace the write path prefixed onto it — there is nothing here
  to read it back off.
- **A record that cannot be placed is REFUSED**, named in
  `*FactorMigrationRefusedError`, logged, and left exactly where it is. It is
  never placed in a guessed tenant.
- **A refusal is per record and the run completes.** One record must never
  decide the fate of the others, or of every other TENANT: a refused record
  resolves for nobody — the fail-closed outcome — while everything placeable is
  placed and the service serves.
- **Refusals are TERMINAL or TRANSIENT, and the reason says which.**
  `refusalIsTransient` is the single authority and lists every reason;
  `FactorMigrationRefusal.Transient` carries the answer so no call site
  re-derives it.

  | Reason | Kind | Meaning |
  |---|---|---|
  | `ErrFactorOwnerUnindexed` | terminal | the owning user has no claim in the identity index |
  | `ErrFactorOwnerAbsent` | terminal | the record names no user at all |
  | `ErrFactorRecordUnreadable` | terminal | the value is absent or is not an `AuthFactor` |
  | `ErrFactorOwnerTenantUnreachable` | **transient** | the owning tenant is KNOWN; its store is not reachable from this node right now |

- **An unreachable destination is not a store failure.** A self-managed barrier
  tenant is registered **sealed at creation** and stays sealed until its admin
  unseals it, and a node that does not host a tenant's barrier has none
  registered until key delivery arrives. Both are designed states. Classified as
  a store failure, one such record aborts the run — and because the run gates
  the routed service, it takes the login surface down for **every tenant on the
  node**, permanently, since nothing deletes the record and every retry reaches
  it again. It is a per-record refusal instead, and only the unreachable
  tenant's own callers ever see `ErrTenantSealed`.
- **The run is per record and resumable.** Destination writes happen first and
  the legacy primary is deleted last, so an interruption leaves work that can be
  finished rather than a record only the destination knows about. Values are
  copied verbatim, so a field a newer build wrote survives a run by an older
  one. Index entries naming a record that no longer exists are pruned.

## Error semantics

Sentinels (`pkg/pin/errors.go`):

| Sentinel | Meaning |
|---|---|
| `ErrPINNotSet` | not configured yet |
| `ErrPINInvalid` | does not match the stored hash |
| `ErrPINTooShort` | shorter than the 6-character minimum |
| `ErrPINAlreadySet` | `Set*PIN` when already configured — use `Change*PIN` |
| `ErrInvalidCurrentPIN` | `Change*PIN` current PIN did not verify |
| `ErrSOPINRequired` | operation needed SO authorization, none supplied |
| `ErrPINLocked` | backend locked out |
| `ErrHierarchyAuthMismatch` | TPM hierarchy auth from a session whose local state was lost — factory reset required |
| `ErrStrategyNotSet` | `Service` invoked with no backend |
| `ErrStateCorrupted` | persisted `PINRecord` did not decode |
| `ErrUnsupportedHashAlgorithm` | neither `argon2id` nor `pbkdf2` |

Factor sentinels (`pkg/auth/factors_errors.go`), relevant here:

| Sentinel | Meaning |
|---|---|
| `ErrReservedMetadata` | caller-supplied metadata carried a key inside the server-owned `attest_` namespace; carried by `*ReservedMetadataKeyError`, which names the key and the prefix |
| `ErrInvalidAttestation` | an `AttestationRecorder` call named a level outside `none` / `software` / `hardware`; carried by `*InvalidAttestationError`, which names the level. Never crosses a wire — the recorder is in-process only |

Typed errors carrying payload:

| Type | Payload |
|---|---|
| `ErrTPMLocked` | `*LockoutStatus` — hardware lockout with recovery time |
| `ErrPINInvalidWithStatus` | `*LockoutStatus` — wrong PIN with attempt-counter info |
| `ErrStoragePersistFailed` | `Key` plus the wrapped cause |
| `ErrStorageLoadFailed` | `Key` plus the wrapped cause (other than not-found) |
| `ErrUnsupportedPBKDF2Hash` | the unsupported hash name |

**Every one of these fails closed.** A PIN that cannot be verified is not
accepted, and a corrupted record is an error rather than a re-initialization.

## Dual control

Barrier initialization enforces the PIN pair: supplying `--so-pin` requires
`--user-pin` and vice versa, satisfying FIPS 140-3 and PCI-DSS v4 dual control.
Any PIN with `--barrier-strategy=none` is refused with `PINsNotAllowedError` —
see [../barrier-seal/](../barrier-seal/engineering.md).

## Configuration

The PIN backend and the factor registry are **composition-plane**, injected at
server construction. Without `WithPINBackend` the daemon builds a default
`pin.SoftwareBackend` with `AutoDetectHashConfig()` over barrier-protected
storage.

## Test strategy

- **Unit**: each `PINBackend` method against `SoftwareBackend`, the 6-character
  floor enforced in `Service`, constant-time comparison, algorithm resolution
  from the record (FIPS record verified by a non-FIPS config and vice versa),
  CTAP2 hash caching and wipe on `Close`, and state reload at construction.
- **The lockout no-ops are explicitly tested** so their absence stays visible.
- **Error tests** for every sentinel and typed error above.

## Observability

`GetLockoutStatus()` is the operational signal on a hardware backend; on
software it is `nil` by design. Factor enrolment, rotation, removal and login
are audited through [../audit/](../audit/).
