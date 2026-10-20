# Initialization Ceremony — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06
> Conflicts with this document are defects. Fix them here first.

## Exported surface

`sdk/go`:

```go
func (c *Client) Ceremony() transport.CeremonyClient
```

Composition-plane options (Tier 2):

```go
func WithCeremonyCA(ca ceremony.CeremonyCA) ServerOption
func WithServerTLSKey(key crypto.Signer) ServerOption
func WithPINBackend(backend pin.PINBackend) ServerOption
func WithFactorRegistry(reg *authfactors.FactorRegistry) ServerOption
```

`pkg/ceremony`:

```go
func NewCeremonyService(cfg *CeremonyConfig, barrier *seal.StorageBarrier, log *slog.Logger) (*CeremonyService, error)
func (s *CeremonyService) SetCA(ca CeremonyCA)
func (s *CeremonyService) SetFactorRegistry(r *authfactors.FactorRegistry)
func (s *CeremonyService) SetPINBackend(b pin.PINBackend)
func (s *CeremonyService) SetQuorumLimiter(l QuorumLimitFunc)
func (s *CeremonyService) SetAuditLogger(l audit.AuditLogger)
func (s *CeremonyService) Initialize(ctx, ca CeremonyCA, opts *InitializeOptions) (*InitResult, error)
func (s *CeremonyService) SignCSRInit(ctx, username, soPin string, csrPEM []byte, role string) (*SignCSRInitResult, error)
```

`CeremonyService` is also the barrier's `seal.CeremonyDistributor` and
`seal.HolderVerifier`, and serves the holder operations documented in
[../shamir-custody/engineering.md](../shamir-custody/engineering.md).
`NewTenantCeremonyService` builds the same engine for a tenant barrier over the
system ceremony's `HolderDeps` (CA, factor registry, quorum limiter and
compliance audit trail). `SetCA` keeps the first CA wired; `SetAuditLogger`
replaces the trail, so a composer that reopens its trail hands the ceremony
the new one.

The CA seam:

```go
type CeremonyCA interface {
	SignCSR(ctx context.Context, csrPEM []byte, opts *SignCSROptions) (*x509.Certificate, error)
	CACertificate(ctx context.Context) (*x509.Certificate, error)
	CABundle() ([]byte, error)
	IssueCertificate(ctx context.Context, req *IssueCertRequest) (*IssuedCert, error)
}
```

An implementation must issue exactly the `Subject` and `URIs` in
`SignCSROptions`; the CSR contributes only its public key. `ceremony.LocalCA`
is the reference implementation.

`IssueCertificate` certifies `IssueCertRequest.PublicKey`, a key the caller
already holds. The CA never generates, sees or returns a leaf private key —
`IssuedCert` carries only `Certificate` and `CertPEM` — and a request without
a `PublicKey` is a `*ceremony.IssueCertKeyError`. The caller pairs the
certificate with its own key, which may be a `crypto.Signer` on a device.

`ceremony.ServerTLSConfig(ctx, ca, key crypto.Signer, extraSANs...)` builds
the transports' auto-provisioned TLS 1.3 leaf this way: it asks the CA to
certify `key.Public()`, checks the returned certificate binds that key
(`*CertEncodeError` otherwise), and sets `key` itself as the
`tls.Certificate`'s `PrivateKey`, so every handshake signs through it. A nil
`key` fails closed. The server chooses the key: `server.WithServerTLSKey`
(re-exported from `sdk/go`) composes a device-held signer in, and without it
each transport's leaf gets a fresh in-memory ECDSA P-256 key.

> **Correction (2026-10-07):** this block showed `CACertificate()` without
> its `ctx` argument; the interface has taken a context since the handshake
> trust-path work. `IssueCertificate` used to generate the leaf key and
> return it as `IssuedCert.KeyPEM`; that field is deleted.

**The `SignCSR` contract is a MUST, and it is enforced.** The ceremony CA signs
under the node's cluster CA, which the mTLS listener trusts for client logins,
so every certificate it issues is a login claim. Every `CeremonyCA` —
qrdb's `LocalCA` and any CA composed in from outside, such as go-xkms's —
**MUST**:

- sign with `SignCSROptions.RawSubject()` as the certificate's raw Subject:
  `CN=<Username>`, `OU=[<TenantID>, <Profile>]`, one RDN per attribute so the
  tenant stays the first OU. Encoding the same `pkix.Name` through
  `x509.Certificate.Subject` packs the OUs into one sorted DER SET, which can
  put the profile first;
- carry **no** Subject Alternative Name, and take nothing from the CSR but its
  public key, after checking its proof of possession;
- issue a leaf: not a CA, no certificate-signing key usage, key usage
  exactly `SignCSROptions.KeyUsage`, and extended key usages exactly the set
  `SignCSROptions.ExtKeyUsage` names (order aside, no duplicates).

A CA that does not conform is **refused, fail-closed**: the ceremony re-checks
every certificate a `CeremonyCA` returns and records nothing for it, returning
`*ceremony.CertSubjectError` (Subject, SANs or key differ) or
`*ceremony.IssuedCertCapabilityError` (a CA, certificate signing or another
key usage, or another set of extended key usages).

`InitializeOptions.Officers` is the list of initial share holders
(`OfficerConfig{Username, Role, CSRPEM, EncryptionKEM, EncryptionKey,
Factors, Designated}` — the same fields `RegisterOfficerRequest` takes:
`Initialize` is the ONLY system bootstrap, so
it is also the only place a holder can get a separate envelope key, an
extra factor, or a designated factor); `Policy` is the factor policy of
the initial share set; `Enroller` is the authenticated caller, which may not
appear among the holders. `Initialize` registers each officer through
`holderScope.register` — the identical call `RegisterOfficer` makes for
post-initialization enrolment — so there is exactly one registration path,
not two that could drift apart.

`ThresholdInitializer` implementations register through `ThresholdRegistry`;
qrdb ships only `NoopThresholdInitializer`.

## States

`awaiting_init` → `enrolling` → `operational`. Single-admin
(`threshold < 2`) skips `enrolling`. The move to `operational` happens when
the last initial holder claims.

**State survives a restart.** `NewCeremonyService`
derives it at construction from the barrier's own durable, plaintext-readable
records, rather than always starting at `awaiting_init` regardless of
whether this barrier was already taken through the ceremony in an earlier
process — a fresh service over an already-initialized barrier reports
exactly the state it reports from an older one. `barrier.CeremonyOperational`
(the SAME record `EnrollmentOpen` already relies on to survive a restart)
restores `operational` directly, covering single-admin and M-of-N
uniformly. Under Shamir custody ONLY, `barrier.Initialized` — true once
`InitializeShamir` has run, which it does as the LAST barrier-related
effect of an `Initialize` call that has ALREADY fully committed (every
officer validated as a batch and enrolled) — restores `enrolling`.
Single-admin is excluded from that second check on purpose:
`initializeSystemBarrierLocked` brings the barrier up FIRST, before the
admin credential is even issued, so `Initialized` alone cannot tell a
completed single-admin ceremony from one that crashed mid-call, and the
crash case's own retry-after-crash convergence needs the state to stay
`awaiting_init`. No new node-local state file — everything read is already
durable, replicated state.

## Invariants

- **Only the barrier's unseal key is split.** The ceremony enrols holders and
  calls `StorageBarrier.InitializeShamir`; it never splits a PIN or any other
  secret itself.
- **Envelopes are never persisted** and leave a `secure.Secret` only as the
  claim response to their holder.
- **`Store` consumes the caller's slice, wiping it**, and replacing a held
  envelope destroys the old one. Pinned by `pkg/ceremony/share_store_test.go`.
- **`Claim` destroys the enclave** and retains only the fact of the claim.
- **A batch M-of-N `qrdb init` validates every officer PURELY, through the
  SAME checks `enrol` applies to a single live registration, before any of
  them is enrolled.** `validateOfficerEnrolment` is
  the one shared validator (role validity, principal name, self-enrolment,
  CSR presence/parseability/key kind, the encryption KEM/key, and the
  resolved factor set) both `enrol` and the batch pre-flight
  (`validateOfficerCredentials`) call — a prior, partial copy of these
  checks in the batch path (CSR/key/KEM/factors only) let a bad username, a
  bad role, or self-enrolment on a LATER officer slip through, leaving
  every officer before it in the batch already signed and persisted by the
  time `enrol`'s fuller check caught it.
- **`Initialize`'s M-of-N officer loop is transactional for whatever can
  still fail AFTER that pre-flight** — a CA signing fault, a persist I/O
  error, a duplicate-key race against a concurrent enrolment the batch
  pre-flight could not see. A failure rolls back every officer THIS call
  already enrolled, in reverse order, and best-effort un-provisions the
  SO/User PINs through the `pin.ProvisionRollback` optional capability
  (reached by type assertion, like `FIDO2HashVerifier`) when the backend
  supports it. `pin.SoftwareBackend`'s `RollbackProvision` DELETES the
  persisted SO and User PIN records (`pin.Storage.Delete`), not merely its
  own in-memory state, so a later retry is never blocked by a leftover
  record occupying the slot the rolled-back attempt never got to use. Both
  are best-effort: a hardware PIN backend may have no way to undo a native
  provisioning step, in which case a retry supplying the SAME SO/User PINs
  still succeeds (`pin.Provision`'s own idempotent re-verify); only a
  retry that changes either PIN is affected.
- **`ReissueAdminCredential` verifies the SO PIN through
  `pin.RequireProvisionedSOPIN`, not `pin.RequireSOPIN`.** An
  already-initialized ceremony always provisioned its SO
  PIN during `Initialize` (`validateInitOptions` refuses an empty one), so
  admitting an UNPROVISIONED backend here — `RequireSOPIN`'s own documented
  convenience for a brand-new node — could only mean something is wrong.
  `CeremonyService.VerifyReissueSOPIN` exposes the identical check so
  `service.CeremonyServiceWithAdminRebind.CeremonyInitialize` can verify it
  FIRST, before checking whether the admin is already bound or reading the
  admin back at all: checking either of those first would let an
  unauthenticated caller learn the admin's binding state merely by
  attempting a reissue with no PIN, or the wrong one.
- **Holder certificate identity is CA-assigned**; holders are enrolled from a
  CSR only.
- **PINs never reach any store as raw bytes.** `pin.Provision` hashes through
  the backend; SO-PIN checks are the backend's constant-time hash comparison;
  receive buffers are wiped.
- **No PIN backend, no PIN operation**: SO-PIN-gated operations fail closed
  with `pin.ErrBackendNotConfigured`.
- **Every SO PIN check on the node is held to one lockout, in the PIN
  backend, with a counter per caller.** sign-csr, seal and both initialize paths are
  reachable without authentication on the boot-time sealed-window listener,
  and the SO PIN is their only authorization, so each such call is an online
  guess. The lockout is a `pin.PINBackend` decorator (`pin.NewLockoutBackend`)
  that counts failed SO PIN comparisons: `VerifySOPIN`, and `SetSOPIN` with a
  current PIN, `ChangeSOPIN`, `SetUserPIN` and `ResetLockout`. The counting
  is `auth.SOPINLockout`, the one implementation also behind each tenant's SO
  PIN (`auth.SOAuthenticator`). The ceremony keeps no counter of its own.
  - **A counter per caller.** Every check names its caller
    (`auth.CallerPrincipal`): an authenticated identity (not admitted
    anonymously by the sealed window) is its tenant and subject; otherwise a
    TLS-verified client certificate is the digest of its leaf; otherwise a
    caller with no identity or certificate at all -- the sealed-window
    caller, or an awaiting-admin-credential reissue -- is its own remote
    network address, recorded as `"addr:<remote>"` by `auth.WithRemoteAddr`,
    which every transport calls where it first
    builds the request's context, before any gate that might bypass
    authentication entirely. Each distinct principal, address-keyed or
    authenticated, has its own counter, so one unauthenticated source's
    wrong guesses never lock out a different, unrelated source making the
    identical kind of anonymous check -- one caller sharing a listener with
    another cannot deny it a seal, a rekey or a sign-csr. Only a context no
    transport ever decorated with ANY of identity, certificate or remote
    address -- in practice, a caller a test builds by hand -- falls back to
    the single bare anonymous counter (`""`) that predates this address
    keying. The address is keyed by HOST ONLY, with the port stripped and
    an IPv6 host normalized AND masked to its /64 (`auth.remoteHostOnly`):
    keying on host:port would give a rotating ephemeral client port -- or
    a new connection per request behind a pooling reverse proxy -- a FRESH
    counter every time, defeating the isolation this bullet describes
    entirely; keying on the full 128-bit IPv6 address would let whoever
    controls the surrounding /64 -- the standard single-subnet allocation
    for one customer, host or container's egress -- mint an effectively
    unlimited number of distinct addresses within it, each a fresh counter
    for free, the identical defeat one level up.
    **Caveat: behind a reverse proxy that does not forward the original
    client address (no `X-Forwarded-For`/`Forwarded` header is read here),
    every client behind that proxy shares the proxy's own one address, and
    so shares one counter** -- the isolation this bullet describes stops
    at the proxy, not the original client, because `auth.WithRemoteAddr`
    is given whatever the transport's own listener reports as the peer
    address, which for a proxied connection is the proxy itself. A
    deployment that fronts qrdb with a reverse proxy and needs real
    per-client isolation must either terminate TLS at qrdb directly or
    accept that this cap is shared across every client behind the proxy.
    The transports and the service select the
    counter from the request (`pin.ForCaller`, applied inside `pin.RequireSOPIN`,
    `pin.RequireProvisionedSOPIN` and `pin.Provision`). Wrong PINs guessed
    anonymously, or by a tenant user, a share holder or anyone else holding a
    certificate the client CA issued, therefore lock out only that caller: the
    Security Officer's rekey or revocation is still admitted. An officer's own
    wrong PINs lock only the officer.
  - **An aggregate cap.** All principals of one key with a recorded
    identity or certificate together -- an authenticated caller or one
    presenting a TLS-verified client certificate -- may present at most 64
    wrong PINs in a 15-minute window; past that, every such check of the
    key -- the officer's included -- is refused until the window ends.
    **Address-keyed (`"addr:<host>"`) principals never count toward, and
    are never refused by, this cap** -- only an authenticated identity or
    a certificate principal does, for the reason above: an unauthenticated
    caller is reachable with no proof of who it is at all, so letting
    enough distinct hosts guess together lock out a DIFFERENT host, or an
    authenticated officer sharing the aggregate pool, would let anonymous
    noise deny service to a caller the cap is meant to protect.
    **Address-keyed principals instead share their OWN, separate aggregate
    cap** (`auth.SOPINAddrAggregateFailures`/`Window`) -- the identical
    64-in-15-minutes shape, a completely independent budget that never
    adds to or is cleared by the authenticated one. Without it, enough
    distinct address principals
    guessing together -- several /64s, say, each one its own principal
    after the masking above -- could still grind down one address's own
    backoff, or keep minting new tracked `"addr:"` principals fast enough
    to push the per-key tracked-principal cap (below) into eviction
    pressure indefinitely. The single bare anonymous (`""`) principal
    joins neither cap: each address keeps its own per-principal backoff
    plus its cap's shared ceiling, with no cross-cap effect either way.
    One principal's
    own backoff holds it to about 15 failures in a window, so either cap is
    reached only by several principals guessing together. The
    authenticated cap never refuses a **known-good** principal: one
    that has presented the correct SO PIN before -- a concept the address
    cap has no equivalent of (an `"addr:"` principal never joins the
    known-good set at all, see below), so it refuses whichever address
    principal asks once it is held, with no exemption for one that
    previously guessed correctly. Up to 8 known-good
    principals are kept per key, persisted with the PIN backend (the software
    backend's storage) and, for a tenant SO PIN, in the authenticator's store,
    so the exemption survives a restart. What is persisted is a
    domain-separated SHA-256 digest of the key and the principal, so the
    record does not store principal ids. The digest is unkeyed and so not a
    secret: principal ids are guessable, and whoever can read the record can
    confirm a guessed one offline. No lock is held across a store call: the
    set is loaded before the check and saved after it, each call bounded to
    5 s (`auth.SOPINKnownGoodTimeout`), so a slow store -- a degraded Raft
    shard -- delays only the check that asked, never another key's. A save
    reads the stored set and writes the union with its own, so nodes sharing
    the tenant record do not drop each other's entries; a save whose read
    fails writes nothing. The record carries no times, so when more than 8
    principals are known-good across the nodes sharing it, a merge keeps the
    saving node's own entries and drops the stored ones first: a principal
    known-good only through another node can fall out, and is then held to
    the cap (never refused outright) until its next successful check. A load that fails is not retried for 30 s
    (`auth.SOPINKnownGoodRetry`); until then the key's principals are simply
    held to the cap. Each key -- the node, each tenant -- has its own lock,
    held across that key's PIN verification so a failure is counted before
    the key's next check; one tenant's verification never waits on
    another's. An anonymous success resets only the anonymous counter: it
    neither clears the aggregate cap nor makes anyone known-good, so an
    officer that has presented the PIN only anonymously is not exempt from
    the cap until it succeeds once authenticated. A principal authenticated by
    a holder certificate is `holder:<tenant>/<subject>`, never the user of the
    same name (`id:<tenant>/<subject>`): the two share neither a counter nor
    known-good status. A known-good principal still has its
    own backoff, and its failures do not count toward the cap. The residual is
    therefore narrower: colluding certificate holders can delay, for one
    window, only an officer that has never yet presented the correct PIN from
    that principal. At most 4096 principals are tracked per key; when that is
    reached, counters whose lockout has expired and whose last failure is
    older than 15 minutes are evicted first, and only then do new principals
    share one overflow counter.
  - **One decoration per backend.** `pin.NewLockoutBackend` returns the same
    decoration every time it is given the same raw backend, so the ceremony
    and each transport that installs the lockout on one backend share one set
    of counters and one budget of free failures. The backend is recognised by
    its pointer identity, so it must be a pointer: a value-typed backend is
    refused, and every SO PIN check through it fails closed with
    `pin.LockoutBackendKindError` without comparing the PIN, rather than
    getting a fresh budget on each decoration. The lockout reads time from
    `time.Now` unless the decoration is made with `pin.WithLockoutClock`
    (`pin.NewLockoutBackend` and `lifecycle.NewSealStorePINBackend` take it);
    options configure only the first decoration of a backend, the one every
    consumer shares.
  - It is installed once, where the node's backend is chosen:
    `lifecycle.NewSealStorePINBackend` returns the seal-store backend already
    decorated, and `server.WithPINBackend` decorates an injected backend when
    the option is built. `NewLockoutBackend` is idempotent, so the ceremony
    (through lifecycle's `Infra`) and every transport (through the server)
    hold the same decorated instance. A guess spent on a seal, a recovery-key
    generation, an initialize, a sign-csr, a rekey or a revocation therefore
    costs the same, on every transport.
  - Checks run one at a time, and a failure is counted before the next check
    starts, so concurrent guesses are no cheaper than sequential ones.
  - The first 5 failures in a row on a counter cost nothing, so an operator
    can mistype. Every later failure locks that counter for a backoff that
    doubles from 1 s up to 15 min, and a correct PIN resets the counter it was
    checked against. While a counter is locked every SO PIN check on it, the
    right PIN included, is refused with `pin.SOPINLockedError` without
    comparing the PIN.
  - Only a wrong PIN counts (`pin.ErrPINInvalid`, `pin.ErrInvalidCurrentPIN`,
    `pin.ErrPINInvalidWithStatus`). Successful checks, requests that fail
    before the PIN is looked at, and a backend that cannot answer consume
    nothing. Anonymous wrong PINs CAN lock the anonymous counter and so delay
    an anonymous officer's sign-csr or seal; they cannot touch any
    authenticated principal's counter.
  - **Recovery.** Wait the backoff out (`RetryAfter`, also sent as
    `Retry-After` / `RetryInfo` / `retry_after_seconds`) and present the right
    PIN, which resets the counter (and, for an authenticated principal, the
    aggregate cap). An officer locked out anonymously recovers at once by
    authenticating, since that is a different counter.
  - The counters are in memory, so a process restart clears them. A deployment
    that needs a lockout that survives restarts injects a hardware PIN backend
    with native dictionary-attack protection; the decorator then sits in front
    of it.
  - The comparison is the PIN backend's constant-time hash comparison.
  - Rekey start and holder revocation (`CeremonyService.AuthorizeSOPIN`, called
    by the barrier service on every transport) also refuse while no SO PIN has
    been provisioned (`pin.ErrPINNotSet`, through
    `pin.RequireProvisionedSOPIN`), rather than admitting any PIN.
- **Initialization refuses an initialized barrier before it compares a PIN.**
  Both initialize paths provision or verify the supplied PINs through
  `service.ProvisionInitializePINs`, which answers
  `seal.ErrAlreadyInitialized` first when `BarrierInitialized` reports the
  barrier initialized. Otherwise an anonymous caller could tell a right SO PIN
  from a wrong one by the answer on a barrier that can never be initialized
  again. A failure to read the initialization record is returned, not treated
  as "not initialized".
- **The SO PIN needs at least 40 bits of entropy.** With the backoff at its
  cap an attacker gets about 96 guesses a day. Use, for example, 8 random
  characters from a 62-character alphabet (about 47 bits) or a 12-digit
  random number (about 40 bits).
- **Holder enrolment is closed to anonymous callers once the ceremony is
  operational.** `RegisterOfficer` refuses a request the boot-time
  sealed-window listener admitted (`sealedwindow.Anonymous`) with
  `ErrEnrollmentClosed` once `EnrollmentOpen` is false: the ceremony is
  operational, the barrier records it (`MarkCeremonyOperational`), or the
  barrier is initialized. The check runs under the ceremony's lock, in the
  same critical section as the registration.
- **Ceremony and firstboot are both idempotent.**

## Error semantics

| Condition | Behavior | Fails closed? |
|---|---|---|
| Threshold ≥ 2 with no holders | `ErrNoOfficers` | **yes** |
| Threshold above the holder count | `ErrInvalidThreshold` | **yes** |
| Duplicate username in the request | `ErrDuplicateUsername` | **yes** |
| The caller among the holders | `ErrSelfEnrolment` | **yes** |
| Empty SO or User PIN | `ErrInvalidSOPIN` / `ErrInvalidUserPIN` | **yes** |
| No PIN backend | `pin.ErrBackendNotConfigured` | **yes** |
| No ceremony CA (an operator-supplied-TLS node, which has no Key A) | holder enrolment and sign-csr refuse with `ErrCANotInitialized` (FailedPrecondition); every verify-only holder operation is still served | **yes** |
| Ceremony already performed | `ErrAlreadyInitialized` | **yes** |
| Node restarts before every holder claims | unclaimed envelopes are lost; a rekey redistributes | **yes** |
| Wrong SO PIN on sign-csr | `pin.ErrPINInvalid`, from the backend's constant-time hash comparison | **yes** |
| sign-csr while the SO PIN check is locked out (after 5 failures in a row; backoff 1 s doubling to 15 min) | `pin.SOPINLockedError`, surfaced as `service.SignCSRThrottledError` (unwraps to `pin.ErrPINLocked`): `429` / `RESOURCE_EXHAUSTED` on every transport, carrying the wait as REST `Retry-After`, a gRPC / Unix `google.rpc.RetryInfo` status detail, MCP `retry_after_seconds` in the error envelope, and a `google.rpc.RetryInfo` detail in the QUIC error frame's payload (seconds rounded up where whole seconds). The PIN is not checked, so a locked-out call cannot confirm a guess | **yes** |
| Anonymous holder registration after enrolment closed | `ErrEnrollmentClosed` | **yes** |
| Rekey start or holder revocation with the wrong SO PIN, or while the check is locked out | `pin.ErrPINInvalid`; `pin.SOPINLockedError` (`429` / `RESOURCE_EXHAUSTED`, with the wait as REST `Retry-After`, a gRPC / Unix `google.rpc.RetryInfo` status detail, MCP `retry_after_seconds` in the error envelope, and a `google.rpc.RetryInfo` detail in the QUIC error frame's payload) | **yes** |
| Rekey start or holder revocation before an SO PIN is provisioned | `pin.ErrPINNotSet` (`412` / `FAILED_PRECONDITION`) | **yes** |

## Permission model

| Permission | Gates |
|---|---|
| `ceremony:initialize` | initializing the ceremony |
| `ceremony:status` | reading state |
| `ceremony:sign_csr` | issuing a system-tenant HOLDER certificate (`so`/`custodian` only, never a login certificate) under the SO PIN |

Holder permissions are listed in
[../shamir-custody/api.md](../shamir-custody/api.md).

## Persistence

- Holder records: `ceremony/officers/<name>` behind the barrier, public
  material only.
- PIN state: the PIN backend's own store (the node-local seal store for
  `pin.SoftwareBackend`).
- Envelopes: memory only.

## Test strategy

**Integration target:** `make integration-test-ceremony` (no Docker; selection in `test/integration/features.mk`). It runs the officer ceremony, forwarding and follower-forward suites, with the sign-csr and sealed-window flows per protocol. Per-transport accounting is the `ceremony` row of `test/integration/COVERAGE.md`.

- **Unit**: state transitions for both modes; holder enrolment through
  `Initialize` and its refusals; that `Initialize` splits the barrier's unseal
  key and nothing else; `Store` wiping and `Claim` destroying; CA-assigned
  identity; PIN provisioning and fail-closed behaviour.
- **`pkg/ceremony/share_store_test.go` pins the custody invariants.**
- **Idempotence tests** for re-running ceremony and firstboot.

## Observability

Ceremony state is reported by `CeremonyStatus`. Holder claim state comes from
the holder list, which never returns a share. Ceremony operations are audited
through [../audit/](../audit/).
