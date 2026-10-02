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
  (`auth.CallerPrincipal`): one for anonymous sealed-window checks, and one
  per authenticated principal -- the identity, or else the verified client
  certificate's leaf digest -- under an aggregate cap of 64 authenticated
  failures per 15-minute window. No caller can lock another out: neither
  anonymous guesses nor a tenant user's certificate lock out the officer;
  only several principals guessing together can engage the cap, for one
  window, and never against a known-good principal (one that has presented
  the correct PIN before, persisted through `auth.SOPINKnownGoodStore` --
  `SoftwareBackend` implements it -- as domain-separated digests, never
  principal ids, and never read or written under the lockout's lock; each
  store call is bounded by `auth.SOPINKnownGoodTimeout`). A holder
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
- **The CTAP2 hash is cached in an atomic pointer** and wiped on `Close`, so a
  virtual authenticator answers CTAP2 verifications after a restart without a
  separate set call.
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

### The custodian surface is routed too

`service.AuthFactorServicer` (`Client.AuthFactors()`, the ceremony-time
custodian surface `pkg/server` exposes at `/v1/authfactors/*` for consumers
like xkms) used to be the one place left binding to a single flat KV after the
user-side surface above was routed — `pkg/server/server.go`'s
`ensureAuthFactorService` and `cmd/qrdbd/start.go` each built it directly over
a bare `KVServicer`. This is closed the same way the flat construction was
closed for the user-side service above:

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
  table above documents for `Get`/`Remove`/`Rotate`/`Relabel`/`MarkUsed`: the
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
  else is refused with `*service.AuthFactorTenantMismatchError`. An
  acme-context caller who writes `globex` there is exactly the shape this
  catches: silently correcting the field to `acme` was considered and
  rejected, because it would place the enrolment in the context tenant while
  leaving the caller's mistaken belief about where it went uncorrected —
  hiding the caller's bug rather than surfacing it. Refusing is what a caller
  bound to the wrong context, or one that mistyped the tenant, needs to find
  out.
- **No migration.** Records that a build before this change wrote at the flat
  `__sys:authfactor/{id}` key are left exactly where they are, inert. A
  resolved store confines every key under `{tenant}/` — the reserved
  `__sys:` prefix included, no tenant exempt — so those records are not
  reachable through the routed service for **any** tenant, the system tenant
  included. go-qrdb is pre-release alpha and this cutover carries no
  migration guarantee (owner ruling), the same posture
  [../collections/engineering.md](../collections/engineering.md) recorded for
  its own tenant-namespace cutover. An operator who needs a pre-cutover
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

This replaced an earlier design that cached one `authFactorService` per
tenant in the router, keyed first on the resolved store's identity, then on
the tenant id, then a hybrid of both — and each scheme broke a case a
production `tenant.StoreResolver` can legitimately produce: identity-keying
loses track of a non-comparable resolved store or one a resolver rebuilds
fresh on every call; tenant-ID keying misses a later isolation transition.
Every version of that cache was solving the wrong problem — what actually
broke when two calls landed on different service instances was the pending
challenge sitting in ONE instance's memory, invisible to the other. Moving it
into the store both calls already share removes the dependency on instance
identity entirely: the router now builds a fresh `authFactorService` on every
call, with no cache to keep coherent across resolver behavior.

- **The enrollment's factor is authoritative.** `VerifyFactor` consumes the
  challenge, loads the enrollment it names, refuses a revoked enrollment
  (`*authfactors.EnrollmentRevokedError`), refuses a non-empty caller
  `factorName` that differs from `Enrollment.FactorName`
  (`*authfactors.FactorMismatchError`), and then dispatches to the
  enrollment's factor. It used to dispatch to whatever name the caller passed
  and consult the enrollment only when the name was empty, so with two
  factors registered a caller could answer a strong factor's challenge
  through a weaker one. The name is kept on every transport as an assertion
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
  Previously only an explicit `VerifyFactor` ever cleared a challenge record,
  so one nobody ever answered occupied storage indefinitely. Each
  `ChallengeFactor` call now runs a bounded sweep before issuing and
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
  MCP and embedded carried the native type — a six-transport contract
  violation. `persistChallenge` refuses a non-string value with
  `*authfactors.EnrollmentInvalidError` rather than let one ship silently
  divergent; every factor in this repository and in go-xkms already only
  puts strings in `MethodData`, so this closes an opening no shipped factor
  actually uses. It is also the reason `MethodData` must survive the JSON
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
  either surfaces during `ListFactors` — map to HTTP 503 (REST, matching
  `pkg/api/rest/handlers.go`'s IAM mapping), `codes.Unavailable` (gRPC,
  inherited by Unix, matching `pkg/api/grpc/errors.go`), and
  `wire.StatusUnavailable` (QUIC, via the shared `mapServiceError` every
  handler on that transport already routes through, which mirrors gRPC's
  addition row for row). MCP needed no clause of its own — see the
  "classified consistently" bullet above. Embedded preserves whichever typed
  error it received unchanged, as it always does: `tenant.ErrTenantSealed` is
  exported to the SDK as `sdk.ErrTenantStoreSealed` in
  `sdk/go/types_tenant.go` (deliberately not reusing `sdk.ErrTenantSealed`,
  which aliases the distinct `pkg/seal.ErrTenantSealed` raised by barrier
  operations rather than store resolution — conflating the two would have let
  `errors.Is(err, sdk.ErrTenantSealed)` silently never match this condition),
  and `seal.ErrSealed` is separately exported as `sdk.ErrSealed` already, so
  an embedded caller recognizes both forms through the SDK without importing
  either internal package.

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

## Observability

`GetLockoutStatus()` is the operational signal on a hardware backend; on
software it is `nil` by design. Factor enrolment, rotation, removal and login
are audited through [../audit/](../audit/).
