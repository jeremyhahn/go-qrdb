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
- **qrdb never interprets `FactorEnrollment.Data`.**
- **The qrdb CLI performs no hardware verification.** `auth login` resolves a
  `(method, lookup key)` tuple to a user through the `LoginAuthFactor` RPC.

## Factor types and lookup keys

| Method | Lookup key |
|---|---|
| `mtls` | SHA-256 of the certificate DER |
| `fido2` | WebAuthn `credentialID` bytes |
| `piv` | SHA-256 of the slot certificate DER |

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
