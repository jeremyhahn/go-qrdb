# Authentication Factors and PINs — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-22
> Conflicts with this document are defects. Fix them here first.

## Exported surface

`sdk/go`:

```go
func (c *Client) AuthFactors()     transport.AuthFactorClient      // ceremony-time custodian surface
func (c *Client) UserAuthFactors() transport.UserAuthFactorClient  // user-side N-factor; NIL if unsupported
func (c *Client) Factors()         transport.FactorClient          // nil if unsupported
func (c *Client) Login()           transport.LoginClient           // nil if unsupported
func (c *Client) Sessions()        transport.SessionClient         // nil if unsupported
```

Composition-plane options (`pkg/server`, Tier 2):

```go
func WithFactorRegistry(reg *authfactors.FactorRegistry) ServerOption
func WithPINBackend(backend pin.PINBackend) ServerOption
```

Conditional add (`pkg/auth`, `pkg/service`, re-exported by `sdk/go`):

```go
// pkg/auth
type FactorAddCondition struct {
	NoActiveOfType  bool    // refuse while the user holds an active factor of the type
	ExpectedVersion *uint64 // refuse unless the factor set is at this version
}
func FactorAddConditionOf(noActiveOfType bool, expectedVersion *uint64) *FactorAddCondition // nil when neither is set
func (c FactorAddCondition) Validate() error // refuses an empty condition
func RefuseRotationCondition(cond *FactorAddCondition) error
type FactorConditionFailedError struct{ UserID; Type; Condition; ExpectedVersion; CurrentVersion; ExistingFactorID }
var ErrFactorConditionFailed error // sentinel FactorConditionFailedError matches

// pkg/service — UserAuthFactorServicer methods and helpers
AddConditional(ctx, userID, factor, cond auth.FactorAddCondition) (uint64, error)
FactorSetVersion(ctx, userID) (uint64, error)
func AddAuthFactor(ctx, svc, userID, factor, cond *auth.FactorAddCondition) (uint64, error)
func ListAuthFactors(ctx, svc, userID) ([]*auth.AuthFactor, uint64, error)
type FactorSetContendedError struct{ UserID uint64; Attempts int } // unwraps to ErrFactorSetContended
var ErrUserAuthFactorAtomicStoreRequired error

// sdk/go/transport
AuthFactorAddRequest.IfNoActiveType           bool
AuthFactorAddRequest.ExpectedFactorSetVersion *uint64
AuthFactorAddResponse.FactorSetVersion        uint64 // 0 for an unconditional add
AuthFactorListResponse.FactorSetVersion       uint64
```

`sdk/go` re-exports `ErrFactorConditionFailed`, `FactorConditionFailedError`,
`FactorAddCondition`, `FactorConditionKind`, `FactorConditionNoActiveOfType`
and `FactorConditionExpectedVersion` as aliases, so `errors.Is` and
`errors.As` match the canonical declarations. Per-transport field names are in
[api.md](api.md#conditional-add--the-one-time-claim-and-the-expected-version).

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
func AutoDetectHashConfig() HashConfig  // go-quicraft's seal.FIPSEnabled() decides

const FIDO2PINHashSize = 16 // CTAP2 mandate: SHA-256(PIN)[:16]
func ComputeFIDO2PINHash(pin string) []byte
func VerifyFIDO2PINHash(expected, actual []byte) bool

type FIDO2HashVerifier interface{ /* optional PINBackend capability */ }
func (s *Service) VerifyFIDO2Hash(hash []byte) bool
```

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
  no-op.** The backend itself has no attempt counter. Do not read the nil as
  "not locked out" in a security decision.
- **The SO PIN lockout is a decorator, `pin.NewLockoutBackend`.** It counts
  failed SO PIN comparisons (`VerifySOPIN`; `SetSOPIN` with a current PIN;
  `ChangeSOPIN`; `SetUserPIN`; `ResetLockout`, which it verifies itself
  because the software backend's no-op answers nil for any PIN) and refuses
  every one of them with `SOPINLockedError` (unwraps to `ErrPINLocked`) while
  locked: 5 free failures, then a backoff doubling from 1 s to 15 min. It is
  idempotent and is installed where a node chooses its backend
  (`lifecycle.NewSealStorePINBackend`, `server.WithPINBackend`), and again by
  every transport's own option -- `WithPINBackend` on the REST, gRPC, Unix,
  QUIC and MCP servers, gRPC's `WithRegisterPINBackend`, and
  `embedded.WithPINBackend` -- and by `CeremonyService.SetPINBackend`, so a
  raw backend handed straight to any of them is still throttled, and the
  ceremony and every transport share it. It keeps **a counter per caller**
  (`auth.CallerPrincipal`): one per anonymous sealed-window source host
  (`addr:`, an IPv6 address masked to its /64; the bare anonymous counter
  when no remote address is recorded), and one per authenticated principal --
  the identity, or else the verified client certificate's leaf digest. The
  authenticated principals and the `addr:` principals each have their own
  aggregate cap of 64 failures per 15-minute window. No caller can lock another out: neither
  anonymous guesses nor a tenant user's certificate lock out the officer;
  only several principals guessing together can engage the cap, for one
  window, and never against a known-good principal (one that has presented
  the correct PIN before, persisted through `auth.SOPINKnownGoodStore` --
  `SoftwareBackend` implements it -- as unkeyed domain-separated digests, so
  the record does not store principal ids, read and written under no lock and
  merged with the stored set on save; each store call is bounded by
  `auth.SOPINKnownGoodTimeout`). Checks of different keys run concurrently;
  one key's checks run one at a time. A holder
  certificate's principal (`holder:<tenant>/<subject>`) is distinct from the
  user of the same name. Stale per-principal counters are evicted
  before new principals fall back to the shared overflow counter, and
  `NewLockoutBackend` keeps one decoration per raw backend, recognised by
  pointer identity: a backend that is not a pointer is refused, and its SO
  PIN checks fail closed with `pin.LockoutBackendKindError`. `pin.ForCaller(ctx, backend)` selects the counter; the
  helpers `RequireSOPIN`, `RequireProvisionedSOPIN` and `Provision` take a
  context and apply it, and a backend reached without one is held to the
  anonymous counter. The counting itself is `auth.SOPINLockout`, which also
  holds each tenant's SO PIN (`auth.SOAuthenticator.VerifySOPIN`, keyed by
  tenant) to the same schedule. Over a backend with no native lockout
  `GetLockoutStatus` reports the decorator's own state for the counter the
  view checks against. Recovery is waiting the backoff out (the error carries
  `RetryAfter`) and presenting the right PIN, which resets that counter.
- **`SoftwareBackend` loads existing records at construction**, so
  `SOPINSet()` / `UserPINSet()` report prior state correctly after a restart.
- **The CTAP2 hash is cached in memory in an atomic pointer** and zeroed on
  `Close`. After a restart the first successful user PIN verification
  repopulates it, so a virtual authenticator answers CTAP2 verifications
  without a separate PIN change.
- **Concurrent reads on `SoftwareBackend` use atomic pointers, no locks.**
- **Passing `nil` for `Storage` gives in-memory-only operation** — for tests.
- **`Client.UserAuthFactors()` returns nil** on a transport without the
  interface. Every transport, including embedded, implements
  `transport.UserAuthFactorClient` alongside the ceremony-time
  `AuthFactorClient` surface. `Factors()`, `Login()` and `Sessions()`
  likewise. **Nil-check them anyway** — a nil is not an error value, and the
  two auth-factor surfaces are independent capabilities that a future
  transport is not guaranteed to carry both of.
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
- **A conditional add is decided by one guarded write on the factor-set
  record** (`__sys:auth/factorsets/{userID}`, JSON `{"version":N,"active":{id:type}}`),
  never by a read the caller or the handler made first. The record is
  committed with `PutIfAbsent` when absent and `CompareAndSwap` against the
  exact bytes read otherwise; no Raft opcode exists for it. Of N concurrent
  adds racing one claim, exactly one commits.
- **Every membership change moves the version.** `Add`, `AddConditional`,
  `Remove`, `Delete` and `Rotate` all commit the set, so a version read from `List`
  is stale as soon as anything a condition could depend on changes. Relabel
  does not change membership and does not move it.
- **The version is checked before the type**, so a caller holding a stale
  version learns that first, whatever else it asked.
- **The commit loop is bounded at 64 attempts.** Each refused swap means another
  change to the same user committed in between; reaching the bound returns
  `*service.FactorSetContendedError` and the caller retries.
- **The new factor's primary record is reserved before the set commits and
  released with `CompareAndDelete` when the add is refused.** A caller may
  name the factor id, so the record is written behind an absence guard
  (`PutIfAbsent`, or the batch guarding the key's absence): an id already in
  use, by this user or any other, is refused with
  `*auth.FactorAlreadyExistsError` before anything is written, and the
  release can only remove a record this add created. A store with neither
  guard reads the key first, and an add there is never released. The user and
  lookup indexes commit in the SAME multi-guarded batch as the factor set
  (`commitEnrollment`), guarded on the set record the decision was made
  against, on the primary record still holding the bytes the add wrote, and
  on every index key being absent. A hard `Delete` commits over the same
  primary and set record, so the two serialise: a `Delete` that lands first
  refuses the add with `*auth.FactorNotFoundError` and no index written, one
  that lands after removes the indexes the add wrote. An index claimed by
  another node since the duplicate check refuses the add with
  `*auth.FactorAlreadyExistsError`. Writing the indexes after the commit
  would let them outlive a `Delete` landing in between, naming a deleted
  factor and keeping its credential reserved. On a store with only a
  single-key guard the indexes follow the commit; such a store refuses a hard
  `Delete`, and each commit re-derives the active set from the record's ids
  plus the user index, reading each primary record, so a process that stops
  between the commit and the index writes is repaired by the next commit.
- **`ListAuthFactors` reads the version before the factors**, so a version it
  reports can only be older than the list, never newer: an add conditioned
  on it is refused rather than accepted against a set the caller never saw.
- **A store without guarded writes refuses every conditional add** with
  `service.ErrUserAuthFactorAtomicStoreRequired` and reports version 0. It is
  never silently treated as unconditional.
- **`Delete` is one multi-key guarded batch, and nothing else.** It plans
  from the primary record, the factor set and every index key
  `factorIndexKeys` names (user index, credential lookup, SPKI lookup),
  guards each on the bytes it read, and commits the record delete, the
  index deletes and the next set through
  `kvstore.MultiGuardedBatchKVStore.PutAllIfAllUnchanged` — one proposal.
  A refused batch re-plans, inside the same 64-attempt bound. An index whose
  value names another factor is guarded but not deleted. A revoked factor is
  refused with `*auth.FactorRevokedError` before any write, so its claims
  stay; a store without the batch is refused with
  `service.ErrUserAuthFactorBatchStoreRequired`. It is audited as `delete`
  and authorized as `user_authfactors:add`, the add it undoes.
- **`Remove` and `Rotate` commit their records and the next set together.**
  On a store with the multi-guarded batch, `Remove` writes the revoked
  record and the set without it in one batch, guarded on both as read, with
  the same last-factor rule and 64-attempt bound as `Delete`. `Rotate` writes
  the new record, its index entries, the revoked old record and the set with
  the new factor in and the old one out in one batch. That batch is guarded
  on the old record and the set as read, on the new record being absent, and
  on each new index entry as read. An entry that already names the old
  factor, the SPKI lookup of a same-keypair renewal, is repointed. So a
  failed write leaves neither the revocation nor the version. A hard
  `Delete` of the old factor landing in between refuses the batch, and the
  re-plan returns `*auth.FactorNotFoundError` rather than writing the deleted
  record back as revoked. A new id that names an existing record, or a new
  credential claimed in between, is refused with
  `*auth.FactorAlreadyExistsError`. On a store with only a single-key guard
  each record is written on its own and the set commits last; the new record
  is reserved behind the same absence guard as an add's, so an id in use,
  the old factor's own included, is refused before anything is written.
- **Rotate takes no condition.** A rotation request carrying one is refused by
  `auth.RefuseRotationCondition` on every transport before any write.
- **The qrdb CLI performs no hardware verification.** `auth login` resolves a
  `(method, lookup key)` tuple to a user through the transport's
  `LoginAuthFactor` method.

## Factor types and lookup keys

| Method | Lookup key |
|---|---|
| `mtls` | SHA-256 of the certificate DER |
| `fido2` | WebAuthn `credentialID` bytes |
| `piv` | SHA-256 of the slot certificate DER |
| `pkcs11` | SHA-256 of the SubjectPublicKeyInfo DER |

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
- **The keys** — `__sys:auth/factors/{id}`,
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
| `Get`, `Remove`, `Delete`, `Rotate`, `Relabel`, `MarkUsed` | the **context** tenant | a factor id is issued inside a tenant and only travels back to it; there is no index for it and none is wanted |
| `LookupByCredentialKey` | the **context** tenant — the tenant the login was begun under | this is the isolation boundary; nothing downstream re-checks it |

The write path is stricter than the read path on purpose. A write routed to
the caller's own tenant on a denial would bind a credential of the caller's
choosing to another tenant's user id, leaving the boundary resting on the
downstream user lookup refusing the same cross-tenant read — two components
having to agree. The refusal holds it where the write is.

### The custodian surface is routed too

`service.AuthFactorServicer` (`Client.AuthFactors()`, the ceremony-time
custodian surface `pkg/server` exposes at `/v1/authfactors/*` for consumers
like xkms) is routed the same way as the user-side service above:

```go
func NewAuthFactorService(cfg AuthFactorServiceConfig) (AuthFactorServicer, error)               // ONE tenant's store
func NewRoutingAuthFactorService(cfg RoutingAuthFactorServiceConfig) (AuthFactorServicer, error) // tenant-routed
```

- **`AuthFactorServiceConfig.KV` is an `AuthFactorKV`**, the same three-verb
  interface `UserAuthFactorServiceConfig.KV` uses (reused, not duplicated) —
  `Put`, `Get`, `List` — satisfied verbatim by both `service.KVServicer` and
  `kvstore.KVStore`.
- **There is no owner index.** `Add` on the user-side service routes on the
  tenant that owns a user id because a user id is a globally unique handle
  that can be enrolled from a context other than the one it lives in.
  `EnrollFactor`'s `subject` has no such registry — it is a caller-supplied
  label — and `ChallengeFactor`, `VerifyFactor`, `ListFactors` and
  `RevokeFactor` all key on an enrollment id issued by `EnrollFactor` itself,
  inside the enrolling tenant. That is exactly the "factor id" shape the
  table above documents for `Get`/`Remove`/`Delete`/`Rotate`/`Relabel`/`MarkUsed`: the
  id only ever travels back to the tenant that issued it, so **every verb on
  this service routes on the context tenant**, and a caller presenting
  another tenant's enrollment id gets `authfactors.EnrollmentNotFoundError`
  from its own store rather than a cross-tenant read.
- **Composition roots build the ROUTED service, always** — mirroring the rule
  above verbatim. `pkg/server`'s `ensureAuthFactorService` and
  `cmd/qrdbd`'s `buildAuthFactorServiceOption` both construct nothing without
  a `tenant.StoreResolver`; there is no unrouted fallback.
- **`EnrollParams.TenantID` is checked against the context tenant, never
  trusted as written.** It arrives as caller-supplied data — every transport
  hands `EnrollFactorRequest.TenantID` through with no validation of its own —
  and routing is decided entirely by the context tenant. An empty value is
  filled in from the context; a value matching the context passes; anything
  else is refused with `*service.AuthFactorTenantMismatchError`. The field is
  not silently corrected, because that would hide a caller bound to the wrong
  context or one that mistyped the tenant.
- **No migration.** A record at the flat `__sys:authfactor/{id}` key, with no
  tenant segment, is left where it is, inert. A resolved store confines every
  key under `{tenant}/` — the reserved `__sys:` prefix included, no tenant
  exempt — so such a record is not reachable through the routed service for
  **any** tenant, the system tenant included. An operator who needs that
  custodian enrolment re-runs the enrolment ceremony.

### The custodian surface's pending challenge is persisted, not cached

`authFactorService` (the per-tenant implementation `NewAuthFactorService`
returns, and what the routed service above builds fresh per call) holds no
in-process state at all: a pending challenge between `ChallengeFactor` and
`VerifyFactor` lives in the tenant's own store, under
`__sys:authfactor:challenge/{bucket}/{factorChallengeID}`, a sibling of the
enrollment namespace rather than a child of it so `ListFactors`'s enumeration
never has to filter challenge records back out by type. `{bucket}` is the
challenge's expiry time in whole minutes since the Unix epoch, as a
twelve-digit zero-padded decimal, so key order is expiry order (see the sweep
below).

The challenge id a caller receives is `{bucket}.{factorChallengeID}`: it
carries its own storage location, so `VerifyFactor` derives the exact key
with no lookup. An id without that shape (twelve decimal digits, a `.`, a
non-empty remainder) cannot name any record the service wrote and is refused
as `*authfactors.EnrollmentNotFoundError` before the store is touched. The
stored record keeps the factor's own `ChallengeID`, so `Factor.Verify`
receives the challenge exactly as the factor issued it. A challenge issued
already expired (a zero or negative TTL) is filed in the current bucket rather
than a past one, so no record is ever written behind the sweep; `VerifyFactor`
still refuses it on its own `ExpiresAt`.

The router builds a fresh `authFactorService` on every call and caches none.
Because the pending challenge lives in the store both calls share, a
`ChallengeFactor` and its `VerifyFactor` need not reach the same service
instance.

- **The enrollment's factor is authoritative.** `VerifyFactor` consumes the
  challenge, loads the enrollment it names, refuses a revoked enrollment
  (`*authfactors.EnrollmentRevokedError`), refuses a non-empty caller
  `factorName` that differs from `Enrollment.FactorName`
  (`*authfactors.FactorMismatchError`), and then dispatches to the
  enrollment's factor, so with two factors registered a caller cannot answer
  a strong factor's challenge through a weaker one. The name is kept on every transport as an assertion
  rather than dropped, so a caller that states the wrong factor learns it
  instead of silently being verified against a different one. Names are
  compared through the same normalisation the registry uses to register and
  look a factor up (case-insensitive), so `PIV` and `piv` are one factor and
  can never be judged different, nor two different factors the same. That
  normalisation is `authfactors.NormalizeName`, and both
  `authfactors.FactorRegistry` and `NewAuthFactorService` key factors by it. A
  second factor whose name normalises to a registered one is refused
  (`*authfactors.FactorAlreadyRegisteredError`) rather than silently
  replacing it.
- **Single-use consumption is a guarded delete, not a tombstone.**
  `VerifyFactor` reads the pending challenge and clears it with
  `CompareAndDelete(key, dataJustRead)`, not an unconditional `Put`: a Get
  followed by an unconditional Put lets two concurrent `VerifyFactor` calls
  for the same challenge id both observe it before either clears it, and both
  would then reach `Factor.Verify`, replaying the challenge. Of any number of
  concurrent callers racing the same expected value, the store's atomicity
  guarantees exactly one `CompareAndDelete` succeeds; every other caller
  finds the key already gone and is refused as not-found — the same guard
  `KVServicer.PutIfAbsent` / `kvstore.AtomicKVStore.PutIfAbsent` give the
  create side of a single-use token. A store that cannot perform this
  atomically fails the call with `service.ErrAuthFactorAtomicStoreRequired`
  rather than degrade to the racy Get+Put; every store this service is
  actually deployed over — `KVServicer` (which declares `CompareAndDelete` as
  a required method) and a `kvstore.KVStore` backed by
  `kvstore.AtomicKVStore` — supports it.
- **An unclaimed, expired challenge is reclaimed by later `ChallengeFactor`
  calls, at a cost independent of how many challenges are outstanding.**
  Each `ChallengeFactor` call runs a bounded sweep before issuing and
  persisting its own challenge:
  - A persisted cursor, `__sys:authfactor:challenge-sweep-cursor`, names the
    last bucket whose records have all been reclaimed. The sweep walks forward
    from it only through buckets strictly older than the current bucket minus
    one bucket of grace: every record in such a bucket has expired by at least
    a minute, and the grace absorbs clock skew between the node that filed a
    record and the node sweeping it. A bucket that can still hold a live
    challenge is never listed, so outstanding challenges add nothing to the
    sweep's `List`, `Get` or `CompareAndDelete` count. That is also why the
    sweep can never reclaim the challenge its own call is about to hand back.
  - Each call makes at most one cursor read, 8 `List` calls, 32 record reads
    and guarded deletes, and one cursor write. Because the bucket number is
    fixed-width decimal, a key prefix names an aligned block of 10, 100,
    1000… buckets; each `List` covers the largest such block that lies wholly
    behind the horizon. An absent cursor (the walk starts at bucket zero) or
    a long idle gap is therefore crossed in a handful of calls rather than one
    `List` per minute.
  - Records are processed in key order. When the record budget runs out, or
    a record cannot be reclaimed on this pass (a read fails, or by this node's
    clock it has not expired yet), the cursor stops just before that record's
    bucket and a later call resumes there. Nothing is skipped. A record whose
    bytes are not a challenge is reclaimed too, since nothing could verify it.
  - Every delete is `CompareAndDelete` guarded on the bytes just read, so a
    legitimate `VerifyFactor` racing the sweep for the same record wins or
    loses exactly as a second `VerifyFactor` would. The cursor advances with
    `PutIfAbsent` on the first sweep and `CompareAndSwap` from the exact
    bytes read afterwards. Of two concurrent sweeps, the one writing a stale
    cursor is refused, so the cursor never moves backwards. The work either
    sweep did is idempotent, so the refused one loses nothing.
  - The sweep never fails the caller's `ChallengeFactor`: its failures concern
    records unrelated to the challenge being issued. A store without
    `CompareAndDelete`, `PutIfAbsent` and `CompareAndSwap` is not swept at
    all, rather than swept with unguarded deletes.
  - It runs inline rather than from a background goroutine:
    `authFactorService` has no lifecycle to hang a ticker from (a fresh
    instance is built per call), and the composition roots that build it
    directly do not all share one place to drive a periodic sweep.
- **`Challenge.MethodData` values must be strings, enforced at persist time.**
  The floor is `pkg/api/grpc/proto`'s `ChallengeFactorResponse.method_data`,
  a fixed `map<string, string>` wire schema: a non-string value would arrive
  over gRPC and Unix as whatever string `pkg/api/grpc.authFactorServer`
  stringifies it to (`true`, not the JSON boolean `true`) while REST, QUIC,
  MCP and embedded would carry the native type. `persistChallenge` refuses a
  non-string value with `*authfactors.EnrollmentInvalidError`. It is also the
  reason `MethodData` must survive the JSON
  round trip `persistChallenge`/`consumeChallenge` perform between
  `ChallengeFactor` and `VerifyFactor` (which may run against a different
  service instance) unchanged — see the doc comment on
  `authfactors.Challenge.MethodData`.
- **A sealed tenant's barrier is classified the same way on every transport
  that can reach it, under EITHER of the two sentinels the condition can
  actually arrive as.** `tenant.ErrTenantSealed` is what
  `TenantStoreResolver.Resolve` returns when a barrier-isolated tenant is
  ALREADY sealed; `seal.ErrSealed` is the distinct sentinel the RESOLVED
  store's own `Get`/`Put` return when the barrier seals AFTER `Resolve`
  succeeds but BEFORE the store operation runs — a live per-call check inside
  the barrier itself, not a cached decision, so a store obtained once and
  used for more than one KV call within a single service-layer request (as
  `ChallengeFactor`'s own `loadEnrollment` `Get` then `persistChallenge`
  `Put` sequence does) can observe the seal on whichever call happens to run
  after it. Both — passed through `AuthFactorListReadError` unchanged when
  either surfaces during `ListFactors` — are classed `Unavailable` by the
  shared error table (`pkg/api/errclass`): HTTP 503 on REST,
  `codes.Unavailable` on gRPC (inherited by Unix), and
  `wire.StatusUnavailable` on QUIC. Embedded preserves the typed
  error unchanged: `tenant.ErrTenantSealed` is exported to the SDK as
  `sdk.ErrTenantStoreSealed` in `sdk/go/types_tenant.go`, distinct from
  `sdk.ErrTenantSealed`, which aliases `pkg/seal.ErrTenantSealed` raised by
  barrier operations rather than store resolution; `seal.ErrSealed` is
  exported as `sdk.ErrSealed`. An embedded caller recognizes both forms
  through the SDK without importing either internal package.

### `LastUsedAt` is detached with `context.WithoutCancel`

`LoginResolver.Resolve` updates `LastUsedAt` in a goroutine so a returning
login response cannot cancel it. The detached context is derived with
`context.WithoutCancel`, **not** `context.Background()`: the store is
tenant-routed and reads the tenant off that context, so a background context
would resolve the system tenant, look in the wrong store, and silently stop
advancing `LastUsedAt` for every other tenant. Nothing would fail; the value
would simply stop moving.

### Migrating factor records without a tenant

A factor record at the flat root has no tenant anywhere in the key or in the
record. `service.MigrateFactorsToTenants` moves such records, and the routed service runs it
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
  never placed in a guessed tenant. A caller that receives the error over a
  transport sees 409 / `FAILED_PRECONDITION`: the run is not finished.
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
| `ErrFactorConditionFailed` | a conditional add's condition did not hold; carried by `*FactorConditionFailedError`, which names the user, type, condition kind, expected and current version and, for a type claim, the active factor that holds it. Classified AlreadyExists (409 / `ALREADY_EXISTS`) and carried as the structured refusal `FACTOR_CONDITION_FAILED` in domain `qrdb.v1`; every SDK transport decodes it back into the typed error |

Factor-set errors (`pkg/service/user_auth_factor_set.go`):

| Error | Meaning |
|---|---|
| `*FactorSetContendedError` (unwraps to `ErrFactorSetContended`) | 64 consecutive commits of the user's factor set lost to other changes; classified Aborted (409 / `ABORTED`); retry |
| `ErrUserAuthFactorAtomicStoreRequired` | a conditional add on a store with no guarded writes; unclassified, so 500 / `INTERNAL` — it is a composition fault, not a caller error |
| `*FactorSetDecodeError` | a factor-set or primary record read during a commit did not decode; fails closed |

Typed errors carrying payload:

| Type | Payload |
|---|---|
| `ErrTPMLocked` | `*LockoutStatus` — hardware lockout with recovery time |
| `ErrPINInvalidWithStatus` | `*LockoutStatus` — wrong PIN with attempt-counter info |
| `StoragePersistFailedError` | `Key` plus the wrapped `Cause` |
| `StorageLoadFailedError` | `Key` plus the wrapped `Cause` (other than not-found) |
| `UnsupportedPBKDF2HashError` | `Hash`, the unsupported hash name |

**Every one of these fails closed.** A PIN that cannot be verified is not
accepted, and a corrupted record is an error rather than a re-initialization.

## Dual control

The `qrdb` CLI enforces the PIN pair on `barrier init` and `barrier
init-shamir`: supplying `--so-pin` requires `--user-pin` and vice versa
(FIPS 140-3 and PCI-DSS v4 dual control), refused with `*InvalidArgError`.
On `barrier init`, any PIN with `--barrier-strategy=none` is refused with
`*PINsNotAllowedError` (`cmd/qrdb/errors.go`). Both checks run in the CLI
before any request is sent — see
[../barrier-seal/](../barrier-seal/engineering.md).

## Configuration

The PIN backend and the factor registry are **composition-plane**, injected at
server construction (`server.WithPINBackend`, `server.WithFactorRegistry`).
`qrdbd` and `sdk/go/node` build the node's PIN backend with
`lifecycle.NewSealStorePINBackend` (`pkg/server/lifecycle/pin_store.go`): a
`pin.SoftwareBackend` with `pin.AutoDetectHashConfig()`, persisted in the
node-local seal store — the plaintext store beneath the barrier, readable while
sealed, holding the PIN hashes and never the PINs — and decorated with the SO
PIN lockout.

## Test strategy

**Integration target:** `make integration-test-auth-factors` (no Docker; selection in `test/integration/features.mk`). It runs the `qrdb auth` CLI suite per protocol (`auth_cli_test.go`: login and the factor add, list, relabel, rotate and remove lifecycle, and `auth factor delete` releasing the credential claim while a revoked factor refuses deletion), the conditional-add suite (`auth_factor_condition_cli_test.go`: the one-time claim, the expected version and N racing claims through the CLI per protocol, and the same over embedded through `sdk/go/node`'s in-process client). The factor service's lifecycle (last-factor protection, forced removal, revoked-factor lookup) and its gRPC handlers are unit tests in `pkg/service` and `pkg/api/grpc`. Per-transport accounting is the `auth-factors` row of `test/integration/COVERAGE.md`.

- **Unit**: each `PINBackend` method against `SoftwareBackend`, the 6-character
  floor enforced in both `Service` and `SoftwareBackend`, constant-time comparison, algorithm resolution
  from the record (FIPS record verified by a non-FIPS config and vice versa),
  CTAP2 hash caching and wipe on `Close`, and state reload at construction.
- **The lockout no-ops are explicitly tested** so their absence stays visible.
- **The SO PIN lockout decorator is tested** for the doubling backoff and
  reset, every SO-PIN-comparing method sharing a counter, the anonymous
  caller and each authenticated principal kept apart (neither anonymous
  guesses nor a tenant user's verified certificate lock out the officer),
  the aggregate cap and the bound on tracked principals, concurrent guesses
  counted one at a time,
  idempotent installation (including by `embedded.WithPINBackend` and
  `SetPINBackend`), the per-tenant SO PIN lockout, and -- at the composition
  seams -- that wrong SO PINs spent on a seal lock out a rekey on the same
  counter.
- **Error tests** for every sentinel and typed error above.
- **Conditional add** (`pkg/service/user_auth_factor_set_test.go`): the
  condition holding and failing for each kind, both together, a version
  mismatch, invalid input, a non-atomic store refused, revoked and other
  users' factors not counting, rotated and pre-record factors counting, the
  version moving on every membership change, the bounded commit loop under
  contention, store faults and undecodable records failing closed, and
  `TestAddConditional_concurrentClaimsExactlyOneWins` — N concurrent claims,
  exactly one success. `pkg/auth/factor_condition_test.go` pins validation and
  the refusal's metadata round trip; `pkg/api/errclass/refusal_test.go` the
  classification; `user_auth_factor_condition_test.go` in `pkg/api/grpc`,
  `pkg/api/rest`, `pkg/api/quic` and `pkg/api/mcp` the wire fields and the
  carried refusal; `refusal_test.go` in `sdk/go/transport` and in its `grpc`,
  `rest`, `quic`, `unix` and `mcp` packages the decode back into the typed
  error, and that an unrelated conflict is not decoded as one.

## Observability

`GetLockoutStatus()` is the operational signal on a hardware backend; on
software it is `nil` by design. Factor enrolment, rotation, removal and login
are audited through [../audit/](../audit/).
