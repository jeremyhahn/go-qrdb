# Security Officer Share Distribution Design

> **Status: ARCHIVE — historical record. Do NOT build from this document.**
> Last verified: 2026-10-13 · superseded by `docs/shamir-custody/`.

**Status:** Superseded design record — history, **not** shipped behaviour
**Date:** 2026-06-10

> **Superseded.** This record describes the Security-Officer-only share
> distribution that preceded unified share holders. Its claim flow returned
> raw share bytes, its quorum counted shares rather than authenticated
> holders, and it kept a separate custodian subsystem; all three are gone.
> The contract is [../shamir-custody/](../shamir-custody/). Where this record
> and that folder disagree, the folder is right.
**Applies to:** go-qrdb (all transports, Go SDK, TS SDK, frontend), go-quicbaas (broker + TS SDK + UI)

---

## 1. Problem Statement

Three code paths in qrdb return raw Shamir share material in an API response to the caller
who triggered the operation. This violates the user directive: shares must reach Security
Officers only through the per-SO claim flow, and must never be present in any wire response
outside of a claiming SO's own ClaimShare response.

Confirmed violations:

1. **System barrier init** — `StorageBarrier.initializeShamirDirect` (pkg/seal/shamir.go:80-122)
   returns `ShamirInitResult.Shares`; propagated through `BarrierService.BarrierInitializeShamir`
   → `ShamirInitInfo.Shares` → all transports + Go SDK + TS SDK + ServerInitWizard.svelte.

2. **Tenant barrier init** — `TenantService.tenantBarrierInitShamir`
   (pkg/tenant/service.go:814-851) returns `TenantBarrierInitResult.Shares`; propagated
   through all transports + Go SDK + TS SDK + quicbaas broker + TenantBarrierView.svelte /
   ShamirSharesReveal.svelte.

3. **Rekey** — `StorageBarrier.Rekey` (pkg/seal/rekey.go:30-105) returns
   `ShamirInitResult.Shares` on all transports.

4. **Recovery key generation** — `StorageBarrier.GenerateRecoveryKeys`
   (pkg/seal/recovery.go:44-89) returns `RecoveryKeyResult.Shares` on all transports.

The existing `pkg/ceremony` package already implements the correct model for the system-barrier
initialization ceremony: SO CSR enrollment, per-SO enclave-backed share store, one-time claim
with enclave destruction, officer registry (public metadata only, zero share bytes).

---

## 2. Canonical Home Decision

**Decision: `pkg/ceremony` is the single canonical home. It is extended with per-barrier
scoping (a `barrierScope` parameter) rather than moving anything into `pkg/seal`.**

Justification:

- `pkg/seal` owns the cryptographic barrier and Shamir accumulator. It has no notion of
  identities, certificates, or claim flows. Pushing share distribution into `pkg/seal` would
  cross a layer boundary.
- `pkg/ceremony` already owns: `sealedShareStore`, `OfficerRegistry`, `CeremonyCA`, nonce
  challenge/response, `EnrollExternalOfficer` (sealed-envelope model). The required work is
  additive, not a transplant.
- The system barrier init ceremony already works correctly. Tenant barriers and rekey/recovery
  paths must be brought up to the same model. That means calling into `pkg/ceremony` from the
  tenant and seal service layers, not duplicating the share store.
- Layer ownership is preserved: qrdb composes `pkg/ceremony` into its service layer; quicbaas
  brokers to qrdb via the SDK and never touches ceremony internals.

The system barrier already uses `CeremonyService` for init. The gap is that the non-ceremony
code paths (`initializeShamirDirect`, `Rekey`, `GenerateRecoveryKeys`,
`tenantBarrierInitShamir`) bypass it. This design closes that gap.

---

## 3. Model Choice: Server-Side Claim vs. Sealed Envelope

The ceremony package supports two models:
- **Claim model** (system barrier): `sealedShareStore` holds each share in a `secure.Secret`
  (go-enclave `SealedEnclave`) — AES-256-GCM ciphertext and a wrapped data key on the ordinary
  (unlocked) heap at rest, by design; only the moment a claim opens it does the plaintext exist
  at all, and only then is it mlocked, guard-paged memory, wiped and unmapped the instant the
  call returns. SO authenticates (mTLS thumbprint challenge), claim call returns share once and
  destroys the enclave.
- **Sealed-envelope model** (external officer): share sealed to SO's public key with
  RSA-OAEP / ECIES / X25519; ciphertext returned to the enrolling admin; SO decrypts with
  their private key offline.

**Primary model: claim model.**

Rationale:
- The sealed envelope is returned to the admin who triggered enrollment, not the SO — it still
  allows the share to appear in an API response outside the SO's own session. The SO must then
  separately decrypt it, but the share bytes are in the response wire to the admin.
- The claim model guarantees that the share bytes never appear in any response except to the
  SO's own authenticated session at claim time. This directly satisfies the directive.
- The sealed-envelope model remains available for external/custodian enrollment (existing
  `EnrollExternalOfficer`) where the SO holds a key pair in an external KMS/HSM and there is
  no mTLS session to authenticate against. This conforms already — no change needed.

**For all internal SO enrollment flows, the claim model is mandatory.**

---

## 4. Architecture Overview

```
pkg/ceremony
  CeremonyService               (system barrier, scope="system")
  TenantCeremonyService         (per-tenant barrier, scope="tenant:<id>")
    - reuses sealedShareStore (unchanged)
    - reuses OfficerRegistry   (keyed with tenant-scoped storage prefix)
    - reuses CeremonyCA        (same CA, distinguished by CN/SAN)
    - reuses nonce challenge    (per-scope challenges map)

pkg/seal
  StorageBarrier.InitializeShamir     → calls CeremonyService.SealAndDistribute(...)
  StorageBarrier.Rekey                → calls CeremonyService.RekeyAndDistribute(...)
  StorageBarrier.GenerateRecoveryKeys → calls CeremonyService.RecoveryAndDistribute(...)
  (return types stripped of Shares field)

pkg/tenant
  TenantService.tenantBarrierInitShamir → calls TenantCeremonyService.SealAndDistribute(...)
  TenantBarrierInitResult.Shares        → DELETED

pkg/service
  BarrierService.BarrierInitializeShamir → ShamirInitInfo.Shares DELETED
  BarrierService.BarrierRekey            → RekeyInfo.Shares DELETED
  BarrierService.BarrierGenerateRecoveryKeys → RecoveryKeysInfo.Keys DELETED
```

---

## 5. New Types and Interfaces in pkg/ceremony

### 5.1 BarrierScope

```go
// BarrierScope identifies which barrier a ceremony or officer registry entry
// belongs to. System barriers use ScopeSystem; tenant barriers use
// ScopeTenant("<tenantID>").
type BarrierScope struct {
    kind     scopeKind // system or tenant
    tenantID string    // non-empty only when kind==scopeTenant
}

type scopeKind int

const (
    scopeSystem scopeKind = iota
    scopeTenant
)

// ScopeSystem is the barrier scope for the system (global) barrier.
var ScopeSystem = BarrierScope{kind: scopeSystem}

// ScopeTenant returns a barrier scope for the named tenant.
func ScopeTenant(tenantID string) BarrierScope

// StoragePrefix returns the prefix used for this scope's officer registry
// entries. System: "ceremony/officers/"; Tenant: "ceremony/tenants/<id>/officers/".
func (b BarrierScope) StoragePrefix() string

// LogValue returns a slog.Value for structured logging.
func (b BarrierScope) LogValue() slog.Value
```

### 5.2 TenantCeremonyService

A lightweight shim that carries its own `*sealedShareStore`, scoped
`*OfficerRegistry`, and `BarrierScope`. It embeds no state machine (no
`CeremonyState`, no `pendingCerts` for cert issuance); its sole job is
share sealing, officer registry management, and share claim gating.

```go
// TenantCeremonyService orchestrates per-tenant Shamir share distribution.
// It provides the same claim semantics as CeremonyService but scoped to a
// single tenant barrier. There is exactly one TenantCeremonyService per
// active tenant barrier using the Shamir strategy; it is created by
// TenantService when the barrier is initialized and destroyed when the
// barrier is decommissioned.
type TenantCeremonyService struct {
    mu         sync.Mutex
    scope      BarrierScope
    shareStore *sealedShareStore // same type, not duplicated
    registry   *OfficerRegistry  // same type, scoped storage prefix
    ca         CeremonyCA        // same CA as system, for signing officer CSRs
    challenges map[string]*ChallengeResponse
    logger     *slog.Logger
}

// NewTenantCeremonyService constructs a TenantCeremonyService for the given
// tenant. The registry is backed by the tenant's own barrier storage backend.
func NewTenantCeremonyService(
    tenantID string,
    backend seal.StorageBackend,
    ca CeremonyCA,
    logger *slog.Logger,
) (*TenantCeremonyService, error)

// RegisterOfficer registers a security officer for this tenant's barrier.
// Validates the CSR, signs it with the CA, persists public metadata in the
// officer registry, and returns the signed certificate PEM. Does NOT split
// or store any shares; that happens at SealAndDistribute time.
func (t *TenantCeremonyService) RegisterOfficer(
    ctx context.Context,
    req RegisterOfficerRequest,
) (*RegisterOfficerResult, error)

// ListOfficers returns all enrolled officers for this tenant's barrier.
func (t *TenantCeremonyService) ListOfficers(
    ctx context.Context,
) ([]*OfficerStatus, error)

// SealAndDistribute splits shareSecret into shares for the N enrolled officers
// and places each share into the per-officer enclave in the sealedShareStore.
// shareSecret is wiped before this function returns regardless of outcome.
// Returns the number of shares sealed (== number of enrolled officers).
// Returns ErrInsufficientOfficers if fewer than threshold officers are enrolled.
func (t *TenantCeremonyService) SealAndDistribute(
    ctx context.Context,
    shareSecret []byte,
    threshold, total int,
) (shareCount int, err error)

// BeginClaimShare issues a challenge nonce to the named officer. The officer
// must present a valid mTLS certificate whose thumbprint matches the registry.
func (t *TenantCeremonyService) BeginClaimShare(
    ctx context.Context,
    username string,
    callerCertThumbprint string,
) (*ChallengeResponse, error)

// CompleteClaimShare verifies the officer's nonce signature and returns the
// share bytes exactly once, then destroys the enclave. Returns
// ErrShareAlreadyClaimed on a second call. The returned bytes are the caller's
// responsibility to wipe after use.
func (t *TenantCeremonyService) CompleteClaimShare(
    ctx context.Context,
    username string,
    nonceHex string,
    signature []byte,
    callerCertThumbprint string,
) ([]byte, error)

// OfficerClaimStatus returns a per-officer claim status snapshot (for admin
// visibility). Never includes share bytes.
func (t *TenantCeremonyService) OfficerClaimStatus(
    ctx context.Context,
) ([]*OfficerStatus, error)

// RekeyAndDistribute generates new shares from newShareSecret and seals them
// to the registered officers (which may have changed since init via
// RegisterOfficer / RevokeOfficer). Old unclaimed shares, if any, are
// destroyed before new ones are stored. newShareSecret is wiped on return.
func (t *TenantCeremonyService) RekeyAndDistribute(
    ctx context.Context,
    newShareSecret []byte,
    newThreshold, newTotal int,
) (shareCount int, err error)

// Cleanup destroys all remaining share enclaves and clears challenge state.
func (t *TenantCeremonyService) Cleanup()
```

### 5.3 New Types

```go
// RegisterOfficerRequest carries the parameters for registering a new
// Security Officer against a barrier (system or tenant).
type RegisterOfficerRequest struct {
    Username          string
    CSRPEM            []byte
    AuthenticatorType AuthenticatorType
    AuthenticatorRef  string
}

// RegisterOfficerResult is the outcome of a successful officer registration.
// It carries the signed certificate (to be transmitted to the officer out of
// band) and the officer's share index assignment.
type RegisterOfficerResult struct {
    CertPEM    []byte
    ShareIndex int
}

// OfficerStatus is a non-secret view of an enrolled officer. All fields are
// public metadata; no share material is present.
type OfficerStatus struct {
    Username       string    `json:"username"`
    CertThumbprint string    `json:"cert_thumbprint,omitempty"`
    ShareIndex     int       `json:"share_index"`
    AuthFactor     string    `json:"auth_factor"`
    EnrolledAt     time.Time `json:"enrolled_at"`
    ClaimedAt      time.Time `json:"claimed_at,omitempty"`   // zero = unclaimed
    SharePending   bool      `json:"share_pending"`          // true = sealed, awaiting claim
}

// ShareClaimResult is returned to the SO exactly once upon a successful
// CompleteClaimShare. The share bytes must be wiped by the caller after
// recording or presenting them to the user.
type ShareClaimResult struct {
    ShareIndex int    `json:"share_index"`
    ShareBytes []byte `json:"-"` // never serialized to JSON; caller encodes if needed
}
```

### 5.4 Changes to CeremonyService

`CeremonyService` gains equivalent `RegisterOfficer`, `SealAndDistribute`,
`BeginClaimShare`, `CompleteClaimShare`, `OfficerClaimStatus`, `RekeyAndDistribute`
methods that delegate to the same internal machinery, scoped with `ScopeSystem`.

The existing `InitializeMofN` internal path is refactored to call
`SealAndDistribute` rather than storing shares inline — it already stores them in
`sealedShareStore`, so this is a rename/extract for clarity.

### 5.5 TenantCeremonyRegistry

A new type in `pkg/ceremony` that maps `tenantID → *TenantCeremonyService`. Held
by `TenantService` and lifecycle-managed alongside the tenant barrier.

```go
// TenantCeremonyRegistry manages the set of active TenantCeremonyService
// instances, one per tenant barrier operating under the Shamir strategy.
type TenantCeremonyRegistry struct {
    mu       sync.RWMutex
    services map[string]*TenantCeremonyService
}

func NewTenantCeremonyRegistry() *TenantCeremonyRegistry
func (r *TenantCeremonyRegistry) Get(tenantID string) (*TenantCeremonyService, bool)
func (r *TenantCeremonyRegistry) Set(tenantID string, svc *TenantCeremonyService)
func (r *TenantCeremonyRegistry) Delete(tenantID string)
```

---

## 6. Flow Specifications

### 6.1 Officer Registration

**Precondition:** The admin (system barrier) or tenant-admin (tenant barrier) holds
`ceremony:officer_register` (system) or `tenant_barrier:officer_register`
(tenant, ScopeTenantOnly). The barrier must be unsealed or in the enrolling state.

1. Admin submits `RegisterOfficerRequest{Username, CSRPEM, AuthenticatorType, AuthenticatorRef}`.
2. Server validates: CSR parses and signature checks; username unique in registry; `AuthenticatorType` valid.
3. CA signs the CSR with the custodian profile (ValidityDays=365, KeyUsageDigitalSignature|KeyEncipherment, ExtKeyUsageClientAuth).
4. Share index assigned = `len(existingOfficers) + 1`.
5. `PersistentOfficer` persisted: `{Username, CertThumbprint, PubKeyPEM, ShareIndex, AuthFactor, EnrolledAt}`. No share bytes.
6. `RegisterOfficerResult{CertPEM, ShareIndex}` returned to admin. Admin transmits CertPEM to SO out of band.

**Shares are not split at registration time.** Splitting happens at init/rekey.

### 6.2 System Barrier Shamir Init

**Precondition:** `N >= threshold` officers registered. Caller holds `seal:initialize_shamir`.

1. `BarrierService.BarrierInitializeShamir` calls `StorageBarrier.InitializeShamir`.
2. `InitializeShamir` calls `b.initializeShamirDirect` → generates root key, produces
   `[]string` share values via `ShamirShareSplitter.Split`.
3. `InitializeShamir` immediately calls `CeremonyService.SealAndDistribute(ctx, rootKeyBytes, threshold, total)`:
   - For each officer in registry order, seals `shares[i]` into the officer's `sealedShareStore` enclave.
   - Wipes `rootKeyBytes` and all plaintext share values before returning.
4. Returns `ShamirInitInfo{Threshold, TotalShares}` — **no Shares field**.

### 6.3 Tenant Barrier Shamir Init

**Precondition:** Tenant-admin holds `tenant_barrier:init` (ScopeTenantOnly). `N >= threshold`
tenant officers registered against this tenant's barrier.

1. `TenantService.tenantBarrierInitShamir` initializes the tenant barrier through the tenant's holder authority, `ceremony.ScopedCeremonyService.InitializeShamir`, which refuses registrations and unenrolments while it runs (`ceremony.ErrInitializationInProgress`).
2. Obtained `res` has `res.Shares` (still a `[]string` at this layer — internal only, not returned).
3. Calls `tenantCeremonyRegistry.Get(tenantID).SealAndDistribute(ctx, ...)` with the plain share bytes.
4. Share bytes wiped. Returns `TenantBarrierInitResult{TenantID, Strategy, Threshold, TotalShares}` — **no Shares field**.

### 6.4 SO Authentication and Share Claim

SOs authenticate exclusively via mTLS. The server extracts the client certificate thumbprint
from the TLS context on every request. Claim APIs require that the presented cert thumbprint
matches the officer's `PersistentOfficer.CertThumbprint` in the registry. No SO PIN is
required for claim (the mTLS certificate IS the credential); the SO PIN concept for this flow
is retired.

**Step 1 — BeginClaimShare**

- SO presents mTLS cert.
- Server looks up officer by username.
- Thumbprint of presented cert must match `PersistentOfficer.CertThumbprint`.
- Share must exist in `sealedShareStore` (not yet claimed).
- Server generates 32-byte random nonce, stores with TTL=5min.
- Returns `{Nonce hex, ExpiresAt}`.

**Step 2 — CompleteClaimShare**

- SO signs the nonce with their private key (matching the cert from Step 1).
- Server verifies signature against the officer's stored `PubKeyPEM`.
- On success: `sealedShareStore.Claim(username)` returns share bytes and destroys enclave.
- Share bytes placed in `ShareClaimResult{ShareIndex, ShareBytes}`.
- `OfficerRegistry.MarkClaimed(ctx, username, time.Now())`.
- Response returned **only over the SO's own mTLS session**.
- Share bytes NOT logged, NOT stored anywhere.

**Invariant:** Share bytes traverse only one wire segment: from server to the SO's own
authenticated mTLS session, exactly once.

### 6.5 Unseal Quorum

Unchanged. SOs call `BarrierUnsealShare` (or `TenantBarrierUnsealShare`),
submitting their claimed share value. The `shamirShareAccumulator` holds submitted shares
in go-enclave locked-memory enclaves, deduplicates via SHA-256, reconstructs when threshold met, unseals the
barrier, and wipes all share material. This flow is already correct per the existing
implementation.

### 6.6 Quorum-Gated Sealing

Per the user directive: sealing a Shamir-protected barrier requires M-of-N SO quorum.
A passphrase or none barrier seal remains single-admin.

**New type in pkg/seal:**

```go
// SealVoteAccumulator tracks SO seal votes for a Shamir-protected barrier.
// When threshold distinct votes are accumulated within the TTL, Seal is called
// automatically. The accumulator requires that each vote be accompanied by a
// valid Shamir share submission (reusing the unseal accumulator type for
// consistency), so an SO cannot vote to seal without possessing their share.
type SealVoteAccumulator struct {
    // Internally: same shamirShareAccumulator structure; on threshold-met
    // it calls StorageBarrier.sealInternal() rather than unseal.
}
```

**New method on StorageBarrier:**

```go
// SubmitSealVote submits a Shamir share as a seal vote. When the threshold
// is reached the barrier is sealed and all vote enclaves are destroyed.
// Returns ErrShamirNotConfigured if the barrier is not in Shamir mode.
// Returns ErrAlreadySealed if the barrier is already sealed.
func (b *StorageBarrier) SubmitSealVote(ctx context.Context, shareValue string) (*QuorumProgress, error)
```

**API surface for seal-vote:**

- REST: `POST /v1/barrier/shamir/seal-vote` with `{"share": "<value>"}`.
- REST: `POST /v1/tenants/{id}/barrier/seal-vote` (tenant variant).
- gRPC: `rpc BarrierShamirSealVote(BarrierShamirSealVoteRequest) returns (BarrierShamirSealVoteResponse)`.
- gRPC: `rpc TenantBarrierShamirSealVote(TenantBarrierShamirSealVoteRequest) returns (TenantBarrierShamirSealVoteResponse)`.
- QUIC, MCP, Unix, Embedded: parity per the standard transport rules.

`BarrierSeal` (the existing single-admin seal) checks the active strategy. If strategy is
Shamir, it returns `ErrShamirBarrierRequiresQuorum` instead of sealing. Callers must use the
seal-vote path.

**New permission constants:**

```go
SealSubmitSealVote      Permission = "seal:submit_seal_vote"
TenantBarrierSealVote   Permission = "tenant_barrier:seal_vote"  // ScopeTenantOnly
```

### 6.7 Rekey Flow

**Precondition:** Barrier unsealed. Caller holds `seal:rekey` (system) or
`tenant_barrier:rekey` (tenant, ScopeTenantOnly).
Old quorum having submitted shares to unseal IS the authorization proof (the barrier is
unsealed = old quorum consented). No separate "authorize rekey" quorum is needed beyond
the unseal that preceded it.

New officers may have been registered since the last init (admin can register replacement
SOs at any time). The rekey uses the current enrolled officer set.

1. `StorageBarrier.Rekey(ctx, newThreshold, newTotal)` generates a fresh random
   **unseal key**, AEAD-wraps the (unchanged) root key under it, and calls
   `CeremonyService.RekeyAndDistribute(ctx, unsealKey, ...)` to split the unseal
   key to the current officer set. The re-wrapped blob is persisted *after*
   distribution succeeds (distribute-first / persist-last: a distribution
   failure leaves the old blob and old shares valid, so the rekey can simply be
   re-run with no lockout window).
2. `RekeyAndDistribute` destroys any pending unclaimed share enclaves for the old round,
   resets all `claimed` flags, and seals new shares to each officer in the registry.
3. Returns `RekeyInfo{NewThreshold, NewTotal, ShareCount}` — **no Shares field**.
4. All prior `sealedShareStore` entries for the scope are destroyed before new ones are stored.
5. `OfficerRegistry.ResetClaimedAt(ctx)` clears `ClaimedAt` on all officers so claim-status
   queries show the new round as pending.

**Cryptographic revocation (unseal-key indirection):** because the officers'
shares carry the unseal key rather than the root key, persisting the
re-wrapped blob revokes every previously distributed share — *including
shares already claimed by officers*. A prior-round quorum reconstructs an
unseal key that no longer decrypts the wrapped root key
(`seal.ErrUnsealKeyMismatch`). The root key never rotates and no stored data
is re-encrypted. See `pkg/seal/unsealkey.go` and
[../shamir-custody/architecture.md](../shamir-custody/architecture.md).

**New method on OfficerRegistry:**

```go
// ResetClaimedAt clears the ClaimedAt timestamp on all officers, marking a
// new share distribution round.
func (r *OfficerRegistry) ResetClaimedAt(ctx context.Context) error
```

**Unclaimed share expiry:** If an SO never claims their rekey share, the enclave persists
until `CeremonyService.Cleanup()` is called (graceful shutdown) or a subsequent `Rekey`
clears it. There is no auto-expiry TTL on the sealed share store — shares are not time-limited
because operators may schedule rekey ceremonies across multiple days. The admin can query
`OfficerClaimStatus` to see which SOs have not yet claimed. A future enhancement could add
a configurable claim deadline, but this design does not include it (YAGNI).

### 6.8 Recovery Key Generation

Recovery keys are an emergency fallback for disaster recovery. They follow the same
officer-claim model as init/rekey.

1. Admin calls `GenerateRecoveryKeys(ctx, threshold, total)`.
2. `StorageBarrier.GenerateRecoveryKeys` calls `CeremonyService.SealAndDistribute` with a
   `ScopeRecovery` variant (storage prefix `"ceremony/recovery/officers/"`).
3. Returns `RecoveryKeysInfo{Threshold, Total, ShareCount}` — **no Shares field**.
4. SOs claim their recovery shares via the same `BeginClaimShare`/`CompleteClaimShare` flow,
   specifying scope `"recovery"` in the request (or using a dedicated route, see §7).

**Note on Scope:** Recovery officers may be a different set from the unseal officers.
`GenerateRecoveryKeys` accepts an optional `OfficerSet` parameter; if nil, it reuses the
enrolled officer registry.

---

## 7. API Surface

### 7.1 Deleted Symbols

**pkg/seal:**
- `ShamirInitResult.Shares []string` — field deleted from struct; struct itself renamed
  to `ShamirInitParams` (only Threshold, TotalShares remain).
- `RecoveryKeyResult.Shares []string` — field deleted.

**pkg/service:**
- `ShamirInitInfo.Shares []string` — deleted.
- `RecoveryKeysInfo.Keys []string` — deleted.
- `BarrierService.BarrierInitializeShamir` return type changes: shares absent.
- `BarrierService.BarrierGenerateRecoveryKeys` return type changes: keys absent.
- `RekeyInfo.Shares []string` (if present) — deleted.

**pkg/tenant:**
- `TenantBarrierInitResult.Shares []string` — deleted.

**pkg/api/grpc/proto/qrdbv1/qrdb.proto:**
- `BarrierShamirInitializeResponse.shares` (field 1, `repeated string`) — deleted from message.
- `BarrierShamirInitializeResponse` repurposed as `BarrierShamirOperationResponse` with only
  `threshold` and `total_shares`.
- `BarrierRekeyRequest` response type changed to use the same share-free response.
- `BarrierGenerateRecoveryKeysResponse.keys` — deleted; response carries only threshold/total/share_count.
- `TenantBarrierInitResponse.shares` (field 3, `repeated string`) — deleted.
- `BarrierRootTokenRequest.shares` (field 1) — this is a root-token generation request that
  passes shares; it is KEPT because the SO submits shares for root-token generation (this is
  a deliberate consent signal, not share distribution). No change.

**sdk/go/transport:**
- `BarrierInitializeShamirResponse.Shares []string` — deleted.
- `BarrierRekeyResponse.Shares []string` — deleted.
- `BarrierRecoveryKeysResponse.Keys []string` — deleted.

**sdk/typescript/src/types.ts:**
- `SealInitResult.keyShares` — deleted.
- `BarrierInitShamirResponse.keyShares` (if present) — deleted.

**Frontend (cmd/qrdb/frontend/src/components/ServerInitWizard.svelte):**
- Remove `bootstrapResult.keyShares` handling, share display block (lines 168-244 approx.),
  and `sharesAcknowledged` state. Replace with officer enrollment progress view.

**go-quicbaas (broker layer):**
- `TenantBarrierServicer` implementation: strip Shares from init response.
- `TenantBarrierView.svelte`: remove `ShamirSharesReveal` include; show officer claim status.
- `ShamirSharesReveal.svelte`: DELETE the component entirely.
- TS SDK `TenantBarrierClient`: strip `shares` from `TenantBarrierInitResponse`.

### 7.2 New proto RPCs (qrdb.v1 / qrdb.proto additions)

```protobuf
// Officer management service for system barrier.
service QRDBService {
    // BarrierOfficerRegister registers a new Security Officer for the system barrier.
    rpc BarrierOfficerRegister(BarrierOfficerRegisterRequest) returns (BarrierOfficerRegisterResponse);

    // BarrierOfficerList lists all registered Security Officers for the system barrier.
    rpc BarrierOfficerList(google.protobuf.Empty) returns (BarrierOfficerListResponse);

    // BarrierShareClaimBegin starts the share claim handshake for the calling SO.
    rpc BarrierShareClaimBegin(BarrierShareClaimBeginRequest) returns (BarrierShareClaimBeginResponse);

    // BarrierShareClaimComplete completes the share claim handshake and returns
    // the share exactly once. Only callable by the SO's own mTLS session.
    rpc BarrierShareClaimComplete(BarrierShareClaimCompleteRequest) returns (BarrierShareClaimCompleteResponse);

    // BarrierOfficerClaimStatus returns per-officer claim status (no share data).
    rpc BarrierOfficerClaimStatus(google.protobuf.Empty) returns (BarrierOfficerClaimStatusResponse);

    // BarrierShamirSealVote submits a share as a seal vote (Shamir barriers only).
    rpc BarrierShamirSealVote(BarrierShamirSealVoteRequest) returns (BarrierShamirSealVoteResponse);

    // BarrierRecoveryOfficerRegister registers an SO for recovery key distribution.
    rpc BarrierRecoveryOfficerRegister(BarrierOfficerRegisterRequest) returns (BarrierOfficerRegisterResponse);

    // BarrierRecoveryShareClaimBegin / BarrierRecoveryShareClaimComplete
    // (same shape as the unseal variants, scoped to recovery).
    rpc BarrierRecoveryShareClaimBegin(BarrierShareClaimBeginRequest) returns (BarrierShareClaimBeginResponse);
    rpc BarrierRecoveryShareClaimComplete(BarrierShareClaimCompleteRequest) returns (BarrierShareClaimCompleteResponse);
}

message BarrierOfficerRegisterRequest {
    string username          = 1;
    bytes  csr_pem           = 2;
    string authenticator_type = 3;
    string authenticator_ref  = 4;
}
message BarrierOfficerRegisterResponse {
    bytes cert_pem   = 1;
    int32 share_index = 2;
}
message BarrierOfficerListResponse {
    repeated OfficerStatus officers = 1;
}
message OfficerStatus {
    string username        = 1;
    string cert_thumbprint = 2;
    int32  share_index     = 3;
    string auth_factor     = 4;
    string enrolled_at     = 5;  // RFC3339
    string claimed_at      = 6;  // RFC3339; empty = unclaimed
    bool   share_pending   = 7;
}
message BarrierShareClaimBeginRequest {
    string username = 1;
}
message BarrierShareClaimBeginResponse {
    string nonce_hex  = 1;
    string expires_at = 2;  // RFC3339
}
message BarrierShareClaimCompleteRequest {
    string username   = 1;
    string nonce_hex  = 2;
    bytes  signature  = 3;
}
message BarrierShareClaimCompleteResponse {
    int32  share_index = 1;
    bytes  share_bytes = 2;  // the ONLY place share_bytes appears on the wire
}
message BarrierShamirSealVoteRequest {
    string share = 1;
}
message BarrierShamirSealVoteResponse {
    int32 required   = 1;
    int32 submitted  = 2;
    bool  complete   = 3;
}

// Tenant barrier extension (TenantBarrierService).
service TenantBarrierService {
    // TenantBarrierOfficerRegister registers an SO for a tenant's barrier.
    rpc TenantBarrierOfficerRegister(TenantBarrierOfficerRegisterRequest) returns (BarrierOfficerRegisterResponse);

    // TenantBarrierOfficerList lists officers for a tenant's barrier.
    rpc TenantBarrierOfficerList(TenantBarrierOfficerListRequest) returns (BarrierOfficerListResponse);

    // TenantBarrierShareClaimBegin / TenantBarrierShareClaimComplete
    rpc TenantBarrierShareClaimBegin(TenantBarrierShareClaimBeginRequest) returns (BarrierShareClaimBeginResponse);
    rpc TenantBarrierShareClaimComplete(TenantBarrierShareClaimCompleteRequest) returns (BarrierShareClaimCompleteResponse);

    // TenantBarrierOfficerClaimStatus returns officer claim status for a tenant.
    rpc TenantBarrierOfficerClaimStatus(TenantBarrierOfficerClaimStatusRequest) returns (BarrierOfficerClaimStatusResponse);

    // TenantBarrierShamirSealVote submits a seal vote share for a tenant barrier.
    rpc TenantBarrierShamirSealVote(TenantBarrierShamirSealVoteRequest) returns (BarrierShamirSealVoteResponse);
}

message TenantBarrierOfficerRegisterRequest {
    string tenant_id         = 1;
    string username          = 2;
    bytes  csr_pem           = 3;
    string authenticator_type = 4;
    string authenticator_ref  = 5;
}
message TenantBarrierOfficerListRequest         { string tenant_id = 1; }
message TenantBarrierShareClaimBeginRequest     { string tenant_id = 1; string username = 2; }
message TenantBarrierShareClaimCompleteRequest  { string tenant_id = 1; string username = 2; string nonce_hex = 3; bytes signature = 4; }
message TenantBarrierOfficerClaimStatusRequest  { string tenant_id = 1; }
message BarrierOfficerClaimStatusResponse       { repeated OfficerStatus officers = 1; }
message TenantBarrierShamirSealVoteRequest      { string tenant_id = 1; string share = 2; }
```

### 7.3 REST Routes

**System barrier:**

| Method | Path | Handler | Permission |
|--------|------|---------|------------|
| POST | `/v1/barrier/officers` | `handlerBarrierOfficerRegister` | `seal:officer_register` |
| GET | `/v1/barrier/officers` | `handlerBarrierOfficerList` | `seal:officer_list` |
| GET | `/v1/barrier/officers/claim-status` | `handlerBarrierOfficerClaimStatus` | `seal:officer_list` |
| POST | `/v1/barrier/officers/{username}/claim/begin` | `handlerBarrierShareClaimBegin` | `identity:gated` (sentinel; see §7.6) |
| POST | `/v1/barrier/officers/{username}/claim/complete` | `handlerBarrierShareClaimComplete` | `identity:gated` (sentinel; see §7.6) |
| POST | `/v1/barrier/shamir/seal-vote` | `handlerBarrierShamirSealVote` | `seal:submit_seal_vote` |
| POST | `/v1/barrier/recovery/officers` | `handlerBarrierRecoveryOfficerRegister` | `seal:officer_register` |
| POST | `/v1/barrier/recovery/officers/{username}/claim/begin` | (same shape) | `identity:gated` (sentinel; see §7.6) |
| POST | `/v1/barrier/recovery/officers/{username}/claim/complete` | (same shape) | `identity:gated` (sentinel; see §7.6) |

**Deleted REST routes (raw share output):**

- `POST /v1/barrier/shamir/initialize` — response body loses the `shares` array.
  (Route is kept; response type changes.)
- `POST /v1/barrier/rekey` — `shares` removed from response.
- `POST /v1/barrier/recovery/generate` — `keys` removed from response.

**Tenant barrier:**

| Method | Path | Permission |
|--------|------|------------|
| POST | `/v1/tenants/{id}/barrier/officers` | `tenant_barrier:officer_register` (ScopeTenantOnly) |
| GET | `/v1/tenants/{id}/barrier/officers` | `tenant_barrier:officer_list` (ScopeTenantOnly) |
| GET | `/v1/tenants/{id}/barrier/officers/claim-status` | `tenant_barrier:officer_list` (ScopeTenantOnly) |
| POST | `/v1/tenants/{id}/barrier/officers/{username}/claim/begin` | `identity:gated` (sentinel; see §7.6) |
| POST | `/v1/tenants/{id}/barrier/officers/{username}/claim/complete` | `identity:gated` (sentinel; see §7.6) |
| POST | `/v1/tenants/{id}/barrier/seal-vote` | `tenant_barrier:seal_vote` (ScopeTenantOnly) |

**Deleted tenant REST:**
- `POST /v1/tenants/{id}/barrier/init` response body loses `shares`.

### 7.4 QUIC / MCP / Unix / Embedded

All transports follow the standard parity pattern. Each new gRPC RPC maps to a QUIC
message type, an MCP tool, a Unix subcommand, and an embedded method. The dispatch tables
(`pkg/api/quic/dispatch.go`, `pkg/api/mcp/tools_*.go`, `pkg/api/unix/`) gain the new entries.

For QUIC, each new operation uses the existing `cmd → handler` map pattern with typed
request/response JSON. New QUIC method strings:

```
barrier.officer.register
barrier.officer.list
barrier.officer.claim.begin
barrier.officer.claim.complete
barrier.officer.claim_status
barrier.shamir.seal_vote
barrier.recovery.officer.register
barrier.recovery.officer.claim.begin
barrier.recovery.officer.claim.complete
tenant.barrier.officer.register
tenant.barrier.officer.list
tenant.barrier.officer.claim.begin
tenant.barrier.officer.claim.complete
tenant.barrier.officer.claim_status
tenant.barrier.shamir.seal_vote
```

### 7.5 Go SDK (sdk/go/transport/)

New methods added to the `BarrierClient` interface and each transport implementation
(grpc, rest, quic, mcp, unix, embedded):

```go
// OfficerRegister registers an SO for the system barrier.
OfficerRegister(ctx context.Context, req *transport.OfficerRegisterRequest) (*transport.OfficerRegisterResponse, error)

// OfficerList lists all registered SOs for the system barrier.
OfficerList(ctx context.Context) ([]*transport.OfficerStatus, error)

// OfficerClaimStatus returns per-SO claim status (no share data).
OfficerClaimStatus(ctx context.Context) ([]*transport.OfficerStatus, error)

// ShareClaimBegin issues a challenge nonce to the named SO.
ShareClaimBegin(ctx context.Context, req *transport.ShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error)

// ShareClaimComplete completes the share claim, returning share bytes once.
ShareClaimComplete(ctx context.Context, req *transport.ShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error)

// ShamirSealVote submits a share as a seal vote for Shamir barriers.
ShamirSealVote(ctx context.Context, req *transport.ShamirSealVoteRequest) (*transport.ShamirSealVoteResponse, error)
```

New types in `sdk/go/transport/`:

```go
// OfficerRegisterRequest is the wire type for officer registration.
type OfficerRegisterRequest struct {
    Username          string `json:"username"`
    CSRPEM            []byte `json:"csr_pem"`
    AuthenticatorType string `json:"authenticator_type,omitempty"`
    AuthenticatorRef  string `json:"authenticator_ref,omitempty"`
}
type OfficerRegisterResponse struct {
    CertPEM    []byte `json:"cert_pem"`
    ShareIndex int    `json:"share_index"`
}
type OfficerStatus struct {
    Username       string    `json:"username"`
    CertThumbprint string    `json:"cert_thumbprint,omitempty"`
    ShareIndex     int       `json:"share_index"`
    AuthFactor     string    `json:"auth_factor"`
    EnrolledAt     time.Time `json:"enrolled_at"`
    ClaimedAt      time.Time `json:"claimed_at,omitempty"`
    SharePending   bool      `json:"share_pending"`
}
type ShareClaimBeginRequest   struct { Username string `json:"username"` }
type ShareClaimBeginResponse  struct { NonceHex string `json:"nonce_hex"`; ExpiresAt time.Time `json:"expires_at"` }
type ShareClaimCompleteRequest struct {
    Username   string `json:"username"`
    NonceHex   string `json:"nonce_hex"`
    Signature  []byte `json:"signature"`
}
type ShareClaimCompleteResponse struct {
    ShareIndex int    `json:"share_index"`
    ShareBytes []byte `json:"share_bytes"` // present ONLY in this response type
}
type ShamirSealVoteRequest  struct { Share string `json:"share"` }
type ShamirSealVoteResponse struct { Required int `json:"required"`; Submitted int `json:"submitted"`; Complete bool `json:"complete"` }
```

Tenant-scoped variants add `TenantID string` as the first field.

**Deleted from transport types:**
- `BarrierInitializeShamirResponse.Shares`
- `BarrierRekeyResponse.Shares`
- `BarrierRecoveryKeysResponse.Keys`

### 7.6 Permission Constants (pkg/permission/constants.go additions)

```go
// SealOfficerRegister grants a system admin the ability to register a
// Security Officer for the system barrier.
SealOfficerRegister Permission = "seal:officer_register"

// SealOfficerList grants access to list registered Security Officers and
// their claim status for the system barrier.
SealOfficerList Permission = "seal:officer_list"

// SealSubmitSealVote grants a Security Officer the ability to submit a seal
// vote (share) to collectively seal a Shamir-protected system barrier.
SealSubmitSealVote Permission = "seal:submit_seal_vote"

// TenantBarrierOfficerRegister grants a tenant-admin the ability to register
// a Security Officer for their tenant's barrier. Tenant-scoped (ScopeTenantOnly).
TenantBarrierOfficerRegister Permission = "tenant_barrier:officer_register"

// TenantBarrierOfficerList grants access to list Security Officers for a
// tenant barrier. Tenant-scoped (ScopeTenantOnly).
TenantBarrierOfficerList Permission = "tenant_barrier:officer_list"

// TenantBarrierClaimShare remains defined as a constant (downstream
// composers reference it) but is NO LONGER used to authorize the tenant
// claim routes — those are identity-gated (see below).
TenantBarrierClaimShare Permission = "tenant_barrier:claim_share"

// TenantBarrierSealVote grants a tenant SO the ability to submit a seal vote
// for their tenant's barrier. Tenant-scoped (ScopeTenantOnly).
TenantBarrierSealVote Permission = "tenant_barrier:seal_vote"

// IdentityGated is a deliberately UNREGISTERED sentinel used as the
// transport permission-map value for all officer share-claim routes
// (system, recovery, legacy ceremony custodian, and tenant barrier; all
// four transports). It can never be granted to a role and never appears
// in All(), ByResource, or the UI catalog.
IdentityGated Permission = "identity:gated"
```

**Authorization model for claim routes (AS BUILT — identity-gated, no RBAC perm):**

Security Officers are ceremony-registry principals, not RBAC users: they are
registered during the sealed window, before the Raft-backed user store exists,
so a role-grant-at-registration is structurally impossible and no role
permission can ever authorize them. The claim routes therefore use the
`permission.IdentityGated` sentinel:

- The middleware (`AuthMiddleware.authorize`, shared by REST/gRPC/QUIC/MCP and
  the `AuthorizeTenant` funnel) short-circuits the RBAC PDP for the sentinel:
  it requires an authenticated mTLS identity (no cert → 401, INV-4) and audits
  the decision, nothing more.
- The binding authorization lives at the handler/service layer: the named
  officer must exist in the registry, the presented cert thumbprint must match
  `PersistentOfficer.CertThumbprint` (mismatch → 403, INV-12), and the
  nonce-signature (or factor challenge response, for ceremony custodians)
  must verify. Possession of the enrolled credential IS the authorization —
  the same posture Vault takes for unseal endpoints.
- For tenant claims the thumbprint match is evaluated against the TARGET
  tenant's officer registry, so a cross-tenant claim attempt fails 403 even
  without a ScopeTenantOnly PDP check.

**Role grants:** Add `SealOfficerRegister`, `SealOfficerList` to the Admin role.
Add `SealSubmitSealVote` to the SO role. Add `TenantBarrierOfficerRegister`,
`TenantBarrierOfficerList` to the TenantAdmin role and `TenantBarrierSealVote`
to the TenantSO role. No grant exists or is needed for the claim routes:
`identity:gated` is non-grantable by construction.

---

## 8. UI Impact

### 8.1 ServerInitWizard.svelte (qrdb frontend)

**Delete:**
- `bootstrapResult.keyShares` storage and all rendering (the block lines 168-244 approx).
- `sharesAcknowledged` boolean state and checkbox.
- The "Key Shares" display panel.
- `initializeShamir` call in the SDK client path no longer returns `keyShares`; the field
  is absent from the response. Update the call site to omit the field.

**Add:**
- After Shamir init completes: display an "Officer Enrollment" panel showing a table of
  registered officers and their claim status (from `GET /v1/barrier/officers/claim-status`).
- Each row shows: username, share_index, enrolled_at, claimed (yes/no badge).
- A "Refresh" button polls claim status. Wizard is considered complete when all officers
  have claimed (or admin explicitly acknowledges).
- No share bytes ever appear in this view.

### 8.2 New: OfficerClaimPage.svelte

A standalone page (accessible before full auth if the server is in `enrolling` state)
that an SO navigates to using their newly-issued mTLS certificate. Steps:

1. SO connects with their client cert. The page shows their username and officer info.
2. SO clicks "Claim My Share". The page calls `POST .../officers/{username}/claim/begin`.
3. Server issues nonce. Client-side JavaScript signs the nonce with the SO's key (either
   via SubtleCrypto with the in-browser key or via a PKCS#11/PIV bridge).
4. Page calls `POST .../officers/{username}/claim/complete` with signature.
5. Server returns `{share_bytes}` in the response.
6. Page displays the share bytes exactly once (copy-to-clipboard only). No persistence.
7. SO is shown a "I have recorded my share securely" acknowledgement checkbox.
8. On confirmation, the page clears the share from memory and DOM. Navigation away also
   clears. No re-display is possible.

### 8.3 go-quicbaas TenantBarrierView.svelte / ShamirSharesReveal.svelte

**Delete:**
- `ShamirSharesReveal.svelte` — the entire component. It only exists to display share bytes.
- The `{#if result.shares?.length}` / `<ShamirSharesReveal>` block in `TenantBarrierView.svelte`.
- The `TenantBarrierClient.initShamir()` return type field `shares`.

**Add to TenantBarrierView.svelte:**
- An "Officers" section that shows `GET /v1/tenants/{id}/barrier/officers/claim-status`.
- Same table layout as the system wizard: officer name, share index, claim status.
- An "Add Officer" form (calls `POST /v1/tenants/{id}/barrier/officers`) for the tenant-admin.

---

## 9. Security Invariants and Tests

Every invariant must be enforced by a failing test if violated.

### INV-1: No share bytes in init/rekey/recovery response
`ShamirInitInfo.Shares`, `TenantBarrierInitResult.Shares`, `RekeyInfo.Shares`,
`RecoveryKeysInfo.Keys` must be absent at compile time (field deleted from struct).
**Test:** `TestShamirInitInfo_NoSharesField`, `TestTenantBarrierInitResult_NoSharesField` —
use `reflect.TypeOf(...).NumField()` and `reflect.TypeOf(...).FieldByName("Shares")` to assert
the field does not exist.

### INV-2: Share exists only in a go-enclave locked-memory enclave between split and claim
`sealedShareStore.Store` is the only path that accepts share bytes after `ShamirShareSplitter.Split`.
The share bytes slice passed to `Store` is wiped before the call returns (existing behavior verified).
**Test:** `TestSealAndDistribute_ShareWipedAfterStore` — inject a spy enclave factory, verify
the input slice is zeroed after `Store` returns.

### INV-3: Claim is exactly once per SO
`sealedShareStore.Claim` returns `ErrShareAlreadyClaimed` on second call and leaves no enclave.
**Test:** `TestSealedShareStore_ClaimExactlyOnce` (existing test; verify it remains in
test suite after refactor).

### INV-4: Claim only by matching cert thumbprint
`CompleteClaimShare` (and `TenantCeremonyService.CompleteClaimShare`) return
`ErrCertThumbprintMismatch` if the caller's presented cert thumbprint differs from the
officer's registered `CertThumbprint`.
**Test:** `TestCompleteClaimShare_RejectsWrongThumbprint` — register officer with thumbprint A,
call `CompleteClaimShare` with thumbprint B, assert error.

### INV-5: No share in logs or audit events
Audit events emitted during init/rekey/recovery/claim contain no share bytes.
**Test:** `TestAuditLog_NoShareBytes` — register audit sink, run full init+claim cycle,
assert no event payload contains the known share hex prefix.

### INV-6: Reconstructed key enclave-held and wiped after use
`UnsealWithShare` and `UnsealWithShares` both call `defer secure.Wipe(secret)` before
returning (existing behavior). The `shamirShareAccumulator.Reset()` is called immediately
after reconstruct.
**Test:** `TestUnsealWithShare_SecretWipedAfterUnseal` — inject a barrier spy, verify that
the reconstructed secret bytes passed to unseal are zeroed on the spy's copy.

### INV-7: Accumulator resets after threshold met
`shamirShareAccumulator.Reset()` destroys all share enclaves and clears `seen` map.
**Test:** `TestShamirShareAccumulator_ResetDestroysEnclaves` — add threshold shares, call
Reconstruct, verify `Expired()` is false (it was reset) and that re-adding the same share
returns a new dedup rejection (seen map was cleared).

### INV-8: Seal requires quorum for Shamir barriers
`StorageBarrier.BarrierSeal` (single-admin path) returns `ErrShamirBarrierRequiresQuorum`
when the active strategy is Shamir.
**Test:** `TestBarrierSeal_ShamirRequiresQuorum`.

### INV-9: Persistence contains zero share bytes
After `SealAndDistribute`, scan all keys under `ceremony/officers/` and
`ceremony/tenants/*/officers/` in the barrier backend; assert no stored JSON blob contains
a `"share_value"` or `"shares"` key.
**Test:** `TestOfficerRegistry_NoShareBytesInStorage` — init shamir, call SealAndDistribute,
scan storage backend, assert.

### INV-10: Rekey destroys prior round's enclaves before sealing new ones
`RekeyAndDistribute` calls `shareStore.DestroyAll()` before sealing new shares.
**Test:** `TestRekeyAndDistribute_OldSharesDestroyed` — prime store with one old share,
call RekeyAndDistribute, assert the old share is gone and the new share is present.

### INV-11: `ShareClaimCompleteResponse` is the ONLY proto message containing share_bytes
**Test:** `TestProto_OnlyShareClaimResponseContainsShareField` — iterate all registered
proto message descriptors in qrdbv1, assert that only `BarrierShareClaimCompleteResponse`
and `TenantBarrierShareClaimCompleteResponse` have a field named `share_bytes`.

### INV-12: SO can only claim their own share (identity enforcement)
`BeginClaimShare` and `CompleteClaimShare` reject callers whose mTLS thumbprint does not
match the officer record. The claim routes are identity-gated (`permission.IdentityGated`,
§7.6) — there is no role permission that could authorize a claim — so this handler-layer
thumbprint match is the SOLE authorization and applies to every authenticated caller.
**Test:** `TestShareClaim_SOCannotClaimAnotherSOsShare`.

---

## 10. Bootstrap and Edge Cases

### 10.1 Chicken-and-Egg: First Ceremony Without a Sealed Barrier

The system barrier is sealed (or uninitialized) when SOs must be registered. The officer
registry is backed by the StorageBarrier — sealed state means it cannot write.

Resolution (already handled by `CeremonyService`):
- During the ceremony (`StateEnrolling`), the registry uses an in-memory cache
  (`officerRegistry.cache`) when the barrier is sealed.
- On first unseal (after quorum submission), `CeremonyService.FlushPendingOfficers` writes
  the cached records to the now-unsealed barrier.
- `TenantCeremonyService` uses the same pattern with its own scoped backend.
- Officer CSR registration happens before Shamir init (admin must register N officers, then
  call init). This is the only ordering constraint.

For the very first system boot, the flow is:
1. Admin calls `POST /v1/barrier/officers` for each SO (barrier can be in any state; records
   are cached in-memory if sealed).
2. Admin calls `POST /v1/barrier/shamir/initialize` (threshold, total).
3. Each SO calls `claim/begin` → `claim/complete` using their new mTLS cert.
4. All SOs submit their shares via `POST /v1/barrier/shamir/unseal-share`.
5. Barrier unseals; pending officer records are flushed to barrier storage.

The SO cert is issued at officer registration time (step 1). The SO uses that cert for
steps 3 and 4. This matches the existing `CeremonyService.initializeMofN` model exactly.

### 10.2 Dev Mode: Threshold=1, Total=1

A single-SO configuration (one SO, threshold=1) is valid and flows through the same path:
- Admin registers exactly one officer.
- Init produces one share, sealed to that officer's enclave.
- Officer claims their share.
- Officer submits their one share to unseal.
- Barrier unseals.

No special path exists. The M-of-N ceremony with M=1, N=1 is the supported dev configuration.

**Operational note:** In dev mode (e.g. `storage.backend: memory`) the system-barrier
`CeremonyService` can be pre-configured with a fixed SO cert in the test harness, making the
ceremony flow exercise real code without manual intervention. Integration tests use this pattern.

### 10.3 Quorum TTL During Multi-SO Coordination

SOs do not need to claim simultaneously. Claims are independent: the `sealedShareStore`
holds all unclaimed shares indefinitely (no TTL on the claim window). TTL applies only to
the challenge nonce (5 minutes), not the share itself.

For unseal, the `shamirShareAccumulator` has a configurable TTL (default 5 minutes,
configurable via `QRDB_SEAL_SHARE_TTL` / `seal.share_ttl`). If an SO's submission expires
before all shares are in, SOs must restart the unseal round. This is existing behavior and
unchanged.

For seal votes, the same accumulator TTL applies. SOs coordinate out of band and submit
their votes within the window.

### 10.4 Credential-Mode Shamir

`StorageBarrier.InitializeShamir` has a credential mode (when no `ShamirStrategy` is
registered): the passphrase/credential is split into shares, not the root key. This mode
is also covered by the same officer-claim path: `CeremonyService.SealAndDistribute` is
called with the credential bytes instead of the root key bytes. The distinction is internal
to `InitializeShamir`; the claim flow is identical.

### 10.5 External Officer / Custodian Flows

`EnrollExternalOfficer` already conforms: the sealed envelope is returned to the admin, and
the SO decrypts offline. This is the only permitted exception to the claim-model rule,
and only when the SO has no mTLS session to the server (external PKI/HSM). No change.

`CustodianEnrollment` (pkg/ceremony/custodian_enrollment.go) is unchanged; it handles
groups of external custodians using the same sealed-envelope pattern.

---

## 11. Migration: Files and Symbols to Change

### pkg/seal/shamir.go
- `initializeShamirDirect`: remove `return &ShamirInitResult{Shares: shareVals, ...}`.
  Replace with call to injected `CeremonyDistributor.SealAndDistribute(...)`.
  Return `&ShamirInitParams{Threshold, TotalShares}`.
- `ShamirInitResult` type: delete `Shares []string` field.

### pkg/seal/rekey.go
- `Rekey`: remove `result.Shares = newShareVals`. Call
  `CeremonyDistributor.RekeyAndDistribute(...)`. Return `RekeyInfo{NewThreshold, NewTotal, ShareCount}`.

### pkg/seal/recovery.go
- `GenerateRecoveryKeys`: remove `result.Shares[i] = share.Value`. Call
  `CeremonyDistributor.SealAndDistribute(...)` on the recovery scope. Return
  `RecoveryKeysInfo{Threshold, Total, ShareCount}`.
- `RecoveryKeyResult`: delete `Shares []string` field.

### pkg/seal/barrier.go
- Add `CeremonyDistributor` interface field to `StorageBarrier`.
- `BarrierSeal`: add Shamir strategy check; return `ErrShamirBarrierRequiresQuorum` if active
  strategy is Shamir and quorum not met.
- Add `SubmitSealVote(ctx, shareValue string) (*QuorumProgress, error)`.

### pkg/ceremony/ceremony.go
- Add `RegisterOfficer`, `SealAndDistribute`, `BeginClaimShare`, `CompleteClaimShare`,
  `OfficerClaimStatus`, `RekeyAndDistribute` on `CeremonyService` (scoped to system barrier).
- The existing `initializeMofN` refactors to call `SealAndDistribute` internally.

### pkg/ceremony/ (new files)
- `tenant_ceremony.go` — `TenantCeremonyService`, `TenantCeremonyRegistry`, `BarrierScope`.
- `tenant_ceremony_test.go` — ≥90% coverage.
- `barrier_scope.go` — `BarrierScope`, `scopeKind`, `ScopeSystem`, `ScopeTenant`.
- `officer_status.go` — `OfficerStatus`, `RegisterOfficerRequest`, `RegisterOfficerResult`,
  `ShareClaimResult`.
- `distributor.go` — `CeremonyDistributor` interface (the seam `pkg/seal` imports).

### pkg/ceremony/ceremony.go
- `OfficerRegistry.ResetClaimedAt(ctx context.Context) error` — new method.

### pkg/service/barrier_service.go
- `ShamirInitInfo`: delete `Shares []string`.
- `RecoveryKeysInfo`: delete `Keys []string`.
- `BarrierService.BarrierInitializeShamir` return: adjust callers.
- `BarrierService.BarrierGenerateRecoveryKeys` return: adjust callers.
- Add `BarrierService.BarrierOfficerRegister`, `BarrierOfficerList`,
  `BarrierShareClaimBegin`, `BarrierShareClaimComplete`, `BarrierOfficerClaimStatus`,
  `BarrierShamirSealVote`.

### pkg/tenant/service.go
- `TenantBarrierInitResult`: delete `Shares []string`.
- `tenantBarrierInitShamir`: remove share return; call `TenantCeremonyService.SealAndDistribute`.
- Add `TenantBarrierOfficerRegister`, `TenantBarrierOfficerList`,
  `TenantBarrierShareClaimBegin`, `TenantBarrierShareClaimComplete`,
  `TenantBarrierOfficerClaimStatus`, `TenantBarrierShamirSealVote`.
- Hold `*ceremony.TenantCeremonyRegistry` field; initialize in constructor.

### pkg/api/grpc/service_barrier.go
- `BarrierShamirInitialize`: stop populating `resp.Shares`.
- `BarrierRekey`: stop populating `resp.Shares`.
- `BarrierGenerateRecoveryKeys`: stop populating `resp.Keys`.
- Add handlers for 8 new RPCs: `BarrierOfficerRegister`, `BarrierOfficerList`,
  `BarrierShareClaimBegin`, `BarrierShareClaimComplete`, `BarrierOfficerClaimStatus`,
  `BarrierShamirSealVote`, `BarrierRecoveryOfficerRegister`,
  `BarrierRecoveryShareClaimBegin`, `BarrierRecoveryShareClaimComplete`.

### pkg/api/grpc/tenant_barrier_server.go
- Strip shares from `Init` response.
- Add handlers for 6 new RPCs: tenant officer register/list/claim_begin/claim_complete/
  claim_status/seal_vote.

### pkg/api/rest/handlers_barrier.go
- Strip `Shares` from shamir init, rekey, recovery JSON responses.
- Add 9 new REST handler functions (officer register, list, claim begin, claim complete,
  claim status, seal vote — system + recovery variants).

### pkg/api/rest/server.go
- Register new routes per §7.3.

### pkg/api/quic/handlers_barrier.go, handlers_tenant_barrier.go
- Strip shares from responses.
- Add new QUIC method handlers per §7.4.

### pkg/api/mcp/tools_barrier.go, tools_tenant_barrier.go
- Strip shares from responses.
- Add new MCP tools per §7.4.

### pkg/api/unix/tenant_barrier_cmd.go
- Strip shares; add new commands.

### pkg/api/grpc/proto/qrdbv1/qrdb.proto
- Remove `shares` from `BarrierShamirInitializeResponse`.
- Remove `keys` from recovery response message.
- Remove `shares` from `TenantBarrierInitResponse`.
- Add 18 new messages and 15 new RPCs per §7.2.
- Regenerate pb.go files.

### pkg/permission/constants.go
- Add 7 new permission constants per §7.6.
- Add to role grant maps: Admin, SO, TenantAdmin, TenantSO.

### pkg/auth/middleware/mapping.go
- Add new RPCs to the permission mapping table (default-deny guard requires this).

### sdk/go/transport/barrier_types.go
- Delete `Shares`, `Keys` fields per §7.5.
- Add 8 new request/response types per §7.5.

### sdk/go/transport/client.go
- Add officer/claim/seal_vote methods to the `BarrierClient` interface.

### sdk/go/transport/{grpc,rest,quic,mcp,unix,embedded}/transport_barrier.go
- Implement new methods on each transport.

### sdk/typescript/src/types.ts
- Delete `keyShares` from `SealInitResult`, `BarrierInitShamirResponse`.
- Add `OfficerStatus`, `OfficerRegisterResponse`, `ShareClaimBeginResponse`,
  `ShareClaimCompleteResponse`, `ShamirSealVoteResponse` types.

### sdk/typescript/src/rest.ts, interfaces.ts
- Add officer/claim/seal_vote methods.
- Remove share-returning methods or strip their return types.

### cmd/qrdb/frontend/src/components/ServerInitWizard.svelte
- Per §8.1.

### cmd/qrdb/frontend/src/components/ (new)
- `OfficerClaimPage.svelte` — per §8.2.

### go-quicbaas (separate repo, broker delegation)
- `TenantBarrierServicer` implementation: strip shares from init delegation.
- `TenantBarrierView.svelte`: remove `ShamirSharesReveal`.
- `ShamirSharesReveal.svelte`: DELETE.
- TS SDK `TenantBarrierClient`: update `initShamir()` return type; add officer/claim methods.

---

## 12. Open Risks

### R1: CeremonyDistributor interface import direction
`pkg/seal` must call into `pkg/ceremony` via an interface to avoid a circular import
(`pkg/ceremony` imports `pkg/seal`). The `CeremonyDistributor` interface lives in
`pkg/ceremony/distributor.go`. `pkg/seal` imports only this interface (not the concrete
`CeremonyService`). The concrete wiring happens in `pkg/service/barrier_service.go` or
`cmd/qrdbd`. This is the standard interface-seam pattern used elsewhere in the codebase.

### R2: Tenant CeremonyService lifecycle
`TenantCeremonyService` instances are created when a tenant barrier is first initialized
with Shamir and must survive daemon restarts. On restart, `TenantService` reconstructs
the service from the officer registry (which is persisted behind the tenant barrier). This
requires the same `RestoreFromRegistry` pattern as `CeremonyService`. The tenant barrier
must be unsealed before the registry is readable; the `TenantCeremonyService` is
reconstructed lazily on first officer-related request after unseal.

### R3: Proto field deletion is a wire-breaking change
Deleting `shares` from `BarrierShamirInitializeResponse` and `TenantBarrierInitResponse`
is a backward-incompatible proto change. Old SDK clients that expect to read `shares`
will get empty repeated fields (proto3 zero value). Per the no-legacy-code policy there
are no deprecated shims; old clients break by design. The change requires a coordinated
go-quicbaas SDK update alongside the qrdb release.

### R4: TS SDK sign-nonce in browser
The claim flow requires the SO's browser to sign the challenge nonce with their private key.
For software keys (PKCS#8 PEM in the browser), the `SubtleCrypto.sign` API is sufficient.
For PIV/FIDO2 keys, the browser extension (go-xkms's xKey plugin) handles signing. The
`OfficerClaimPage.svelte` must detect the key type from the officer's registered
`authenticator_type` and route accordingly. This is a UI-layer concern; the server-side
API is key-type agnostic (receives raw signature bytes).

### R5: Admin-only bootstrap before auth is available
If the server's mTLS CA has not been provisioned yet (true first boot), the admin cannot
register officers over mTLS. The solution already exists in `CeremonyService`: the first
admin uses a bootstrap PIN (SO PIN from config) to authenticate the initial CSR signing
(`SignCSRInit`). After the first SO cert is issued, subsequent operations use mTLS. The
tenant ceremony skips this: tenant barriers are always created after system barrier init,
so mTLS is already operational.
