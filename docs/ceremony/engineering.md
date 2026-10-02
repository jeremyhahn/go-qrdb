# Initialization Ceremony — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-29

## Exported surface

`sdk/go`:

```go
func (c *Client) Ceremony() transport.CeremonyClient
```

Composition-plane options (Tier 2):

```go
func WithCeremonyCA(ca ceremony.CeremonyCA) ServerOption
func WithPINBackend(backend pin.PINBackend) ServerOption
func WithFactorRegistry(reg *authfactors.FactorRegistry) ServerOption
```

`pkg/ceremony`:

```go
func NewCeremonyService(cfg *CeremonyConfig, barrier *seal.StorageBarrier, log *slog.Logger) (*CeremonyService, error)
func (s *CeremonyService) SetCA(ca CeremonyCA)
func (s *CeremonyService) SetTrustDomain(trustDomain string)
func (s *CeremonyService) SetFactorRegistry(r *authfactors.FactorRegistry)
func (s *CeremonyService) SetPINBackend(b pin.PINBackend)
func (s *CeremonyService) Initialize(ctx, ca CeremonyCA, opts *InitializeOptions) (*InitResult, error)
func (s *CeremonyService) SignCSRInit(ctx, username, soPin string, csrPEM []byte, role string) (*SignCSRInitResult, error)
```

`CeremonyService` is also the barrier's `seal.CeremonyDistributor` and
`seal.HolderVerifier`, and serves the holder operations documented in
[../shamir-custody/engineering.md](../shamir-custody/engineering.md).
`NewTenantCeremonyService` builds the same engine for a tenant barrier over the
system ceremony's `HolderDeps` (CA, trust domain, factor registry).

The CA seam:

```go
type CeremonyCA interface {
	SignCSR(ctx context.Context, csrPEM []byte, opts *SignCSROptions) (*x509.Certificate, error)
	CACertificate() (*x509.Certificate, error)
	CABundle() ([]byte, error)
	IssueCertificate(ctx context.Context, req *IssueCertRequest) (*IssuedCert, error)
}
```

An implementation must issue exactly the `Subject` and `URIs` in
`SignCSROptions`; the CSR contributes only its public key. `ceremony.LocalCA`
is the reference implementation.

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
(`OfficerConfig{Username, Role, CSRPEM}`); `Policy` is the factor policy of
the initial share set; `Enroller` is the authenticated caller, which may not
appear among the holders.

`ThresholdInitializer` implementations register through `ThresholdRegistry`;
qrdb ships only `NoopThresholdInitializer`.

## States

`awaiting_init` → `enrolling` → `operational`. Single-admin
(`threshold < 2`) skips `enrolling`. The move to `operational` happens when
the last initial holder claims.

## Invariants

- **Only the barrier's unseal key is split.** The ceremony enrols holders and
  calls `StorageBarrier.InitializeShamir`; it never splits a PIN or any other
  secret itself.
- **Envelopes are never persisted** and leave a `secure.Secret` only as the
  claim response to their holder.
- **`Store` consumes the caller's slice, wiping it**, and replacing a held
  envelope destroys the old one. Pinned by `pkg/ceremony/share_store_test.go`.
- **`Claim` destroys the enclave** and retains only the fact of the claim.
- **Holder certificate identity is CA-assigned**; holders are enrolled from a
  CSR only.
- **PINs never reach any store as raw bytes.** `pin.Provision` hashes through
  the backend; SO-PIN checks are the backend's constant-time hash comparison;
  receive buffers are wiped.
- **No PIN backend, no PIN operation**: SO-PIN-gated operations fail closed
  with `pin.ErrBackendNotConfigured`.
- **Every SO PIN check on the node is held to one lockout, in the PIN
  backend, with two counters.** sign-csr, seal and both initialize paths are
  reachable without authentication on the boot-time sealed-window listener,
  and the SO PIN is their only authorization, so each such call is an online
  guess. The lockout is a `pin.PINBackend` decorator (`pin.NewLockoutBackend`)
  that counts failed SO PIN comparisons: `VerifySOPIN`, and `SetSOPIN` with a
  current PIN, `ChangeSOPIN`, `SetUserPIN` and `ResetLockout`. The counting
  is `auth.SOPINLockout`, the one implementation also behind each tenant's SO
  PIN (`auth.SOAuthenticator`). The ceremony keeps no counter of its own.
  - **Two counters.** A check by an authenticated identity (not admitted
    anonymously by the sealed window) or over a TLS-verified certificate
    (`auth.AuthenticatedCaller`) counts against the *authenticated* counter;
    every other check against the *anonymous* one. The transports and the
    service select the counter from the request (`pin.ForCaller`, applied
    inside `pin.RequireSOPIN`, `pin.RequireProvisionedSOPIN` and
    `pin.Provision`). Wrong PINs guessed anonymously therefore lock only the
    anonymous counter: an authenticated Security Officer's rekey or
    revocation is still admitted. An officer's own wrong PINs lock only the
    authenticated counter.
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
    an anonymous officer's sign-csr or seal; they cannot touch the
    authenticated counter.
  - **Recovery.** Wait the backoff out (`RetryAfter`, also sent as
    `Retry-After` / `RetryInfo` / `retry_after_seconds`) and present the right
    PIN, which resets the counter. An officer locked out anonymously recovers
    at once by authenticating, since that is a different counter.
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
| No `CeremonyCA` | `ErrNilCA` | **yes** |
| Ceremony already performed | `ErrAlreadyInitialized` | **yes** |
| Node restarts before every holder claims | unclaimed envelopes are lost; a rekey redistributes | **yes** |
| Wrong SO PIN on sign-csr | `pin.ErrPINInvalid`, from the backend's constant-time hash comparison | **yes** |
| sign-csr while the SO PIN check is locked out (after 5 failures in a row; backoff 1 s doubling to 15 min) | `pin.SOPINLockedError`, surfaced as `service.SignCSRThrottledError` (unwraps to `pin.ErrPINLocked`): `429` / `RESOURCE_EXHAUSTED` on every transport, carrying the wait as REST `Retry-After`, a gRPC / Unix `google.rpc.RetryInfo` status detail, and MCP `retry_after_seconds` in the error envelope (QUIC: message only) (seconds rounded up where whole seconds). The PIN is not checked, so a locked-out call cannot confirm a guess | **yes** |
| Anonymous holder registration after enrolment closed | `ErrEnrollmentClosed` | **yes** |
| Rekey start or holder revocation with the wrong SO PIN, or while the check is locked out | `pin.ErrPINInvalid`; `pin.SOPINLockedError` (`429` / `RESOURCE_EXHAUSTED`, with the wait as REST `Retry-After`, a gRPC / Unix `google.rpc.RetryInfo` status detail, and MCP `retry_after_seconds` in the error envelope (QUIC: message only)) | **yes** |
| Rekey start or holder revocation before an SO PIN is provisioned | `pin.ErrPINNotSet` (`412` / `FAILED_PRECONDITION`) | **yes** |

## Permission model

| Permission | Gates |
|---|---|
| `ceremony:initialize` | initializing the ceremony |
| `ceremony:status` | reading state |
| `ceremony:sign_csr` | issuing a login certificate under the SO PIN |

Holder permissions are listed in
[../shamir-custody/api.md](../shamir-custody/api.md).

## Persistence

- Holder records: `ceremony/officers/<name>` behind the barrier, public
  material only.
- PIN state: the PIN backend's own store (the node-local seal store for
  `pin.SoftwareBackend`).
- Envelopes: memory only.

## Test strategy

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
