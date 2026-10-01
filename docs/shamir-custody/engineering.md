# Shamir Custody — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-29

## Exported surface

`pkg/seal` — the key, the roster and the one quorum accumulator:

```go
type QuorumDomain string // DomainUnseal, DomainRecovery
type QuorumOp string     // OpUnseal, OpSeal, OpRootToken, OpRekey, OpRecover
type HolderRole string   // RoleSecurityOfficer ("so"), RoleCustodian ("custodian")
type FactorPolicy string // FactorPolicyDesignated (default), FactorPolicyAnyEnrolled

type HolderRoster struct { Version, Domain, Policy, Threshold, Total, Distribution, Holders }

func (b *StorageBarrier) SetCeremonyDistributor(d CeremonyDistributor)
func (b *StorageBarrier) SetHolderVerifier(v HolderVerifier)
func (b *StorageBarrier) InitializeShamir(ctx, policy FactorPolicy) (*ShamirInitParams, error)
func (b *StorageBarrier) GenerateRecoveryKeys(ctx, threshold int, policy FactorPolicy) (*RecoveryKeyResult, error)
func (b *StorageBarrier) StartRekey(ctx, domain QuorumDomain, p RekeyParams) (*QuorumRoundInfo, error)
func (b *StorageBarrier) SubmitHolderShare(ctx, sub *HolderSubmission) (*QuorumOutcome, error)
func (b *StorageBarrier) HolderRoster(ctx, domain QuorumDomain) (*HolderRoster, []byte, error)
```

`CeremonyDistributor` (`PrepareRoster`, `Distribute`, `Committed`) and
`HolderVerifier` (`VerifyHolder`) are consumer-side interfaces; `pkg/ceremony`
implements both. `SubmitHolderShare` is the **only** way a share reaches a
quorum: there is no string-share unseal, seal-vote, root-token or recovery
entry point.

`pkg/ceremony` — the holder engine shared by the system, recovery and every
tenant scope:

```go
func (s *CeremonyService) RegisterOfficer(ctx, domain, RegisterOfficerRequest) (*RegisterOfficerResult, error)
func (s *CeremonyService) ListOfficers(ctx, domain) ([]*OfficerStatus, error)
func (s *CeremonyService) BeginClaimShare(ctx, domain, username, callerThumbprint string) (*ChallengeResponse, error)
func (s *CeremonyService) CompleteClaimShare(ctx, domain, username, nonceHex string, sig []byte, callerThumbprint string) (*ShareClaimResult, error)
func (s *CeremonyService) BeginQuorum(ctx, domain, op, username, factor string) (*QuorumChallenge, error)
func (s *CeremonyService) RevokeOfficer(ctx, domain, username string, threshold int) (*seal.QuorumRoundInfo, error)
func (s *CeremonyService) Rebind(barrier *seal.StorageBarrier)
func ParseShareEnvelope(raw []byte) (*ShareEnvelope, error)
func (e *ShareEnvelope) Open(priv *hpke.PrivateKey) ([]byte, error)
func (e *ShareEnvelope) OpenWithKey(priv crypto.PrivateKey) ([]byte, error)
```

Composition (Tier 2), through the server and node options:
`WithCeremonyCA`, `WithFactorRegistry` (hardware factors from go-xkms),
`WithPINBackend`. `sdk/go/node` wires the ceremony service as the barrier's
distributor and verifier, for the sealed window and the full node alike.

`sdk/go` — `transport.BarrierClient` and `transport.TenantClient` carry the
holder methods listed in [api.md](api.md); `sdk/go/transport/embedded`
serves them in-process.

`pkg/seal/sealtest` — `sealtest.Custody` installs an in-memory distributor and
verifier and drives quorum rounds, for tests of code that sits on a Shamir
barrier.

## Holder record

`ceremony.PersistentOfficer`, stored behind the barrier under
`ceremony/officers/` of each scope — public material only:

| Field | Meaning |
|---|---|
| `Username`, `Role` | the holder and `so` / `custodian` |
| `CertPEM`, `CertThumbprint`, `PubKeyPEM` | the CA-issued certificate |
| `EncryptionKEM`, `EncryptionKey` | the envelope recipient key |
| `Factors`, `Designated` | each enrolled factor's public credential; the designated one |
| `EnrolledAt`, `ClaimedAt`, `ShareIndex` | lifecycle |

Records written while the barrier is sealed are cached and written the first
time the barrier is seen unsealed. That is legitimate only **before
initialization**: the initialization ceremony enrols holders before the
barrier exists. On a barrier that is sealed and already initialized,
registration is refused with `ceremony.ErrRegistrationWhileSealed`. The
persisted roster sits behind the barrier, so a sealed scope can neither check
the name nor persist the record. A record cached unchecked would otherwise be
written over the persisted holder of the same name at the next unseal, giving
the registrant that holder's identity.

The reconciliation on the first unsealed access never overwrites a persisted
holder:

- It writes a cached record only when storage has no holder of that name.
- A cached record with the same identity (certificate, signing key and
  envelope key) as the persisted one leaves the persisted record standing.
  Only a claim recorded while sealed, for the same distribution, is carried
  onto it.
- A cached record with a different identity fails with
  `*ceremony.HolderRecordConflictError`.

Unsealed, a registry read that fails for any reason other than "not found" is
an error. It never counts as a free name.

The roster in the wrapped blob, not the record, is what a sealed node
authenticates submissions against.

## Distribution

`redistribute` is the single distribution path, used by `InitializeShamir`,
`GenerateRecoveryKeys`, the encryption migration and every completed rekey:

1. `PrepareRoster` snapshots the active holders (minus any being removed) with
   share indexes 1..N, the policy, the threshold and a random distribution id.
   Initialization refuses a holder count different from the requested total
   (`HolderCountMismatchError`).
2. A fresh 256-bit domain key wraps the root key (AES-256-GCM) with associated
   data `go-qrdb/seal/wrapped-root-key/v2|<RootKeyPath>|<domain>|<roster digest>`;
   the blob stores the roster bytes in clear.
3. `Distribute` splits the domain key and HPKE-seals share *i* to the holder
   with share index *i*. The envelope header — version, KEM, scope, domain,
   holder, share index, threshold, total, distribution id and roster digest —
   is bound into the HPKE info, so an
   edited header makes the envelope unopenable. Every earlier envelope of the
   domain is destroyed first. The domain key is wiped.
4. The blob is persisted, **last**. `Committed` then records share indexes and
   deletes removed holders' records.

A failure before step 4 leaves the previous blob, and the previous share set,
in force; the operation can be re-run.

Envelopes live only in the running ceremony service, so an in-process
data-plane restart (the one a system-scope encryption migration performs)
must keep that service: the daemon and `sdk/go/node` carry it across and
`CeremonyService.Rebind` moves it to the rebuilt barrier, keeping the enrolled
holders, the unclaimed envelopes and the pending challenges.

## Envelopes

- HPKE (RFC 9180) with the holder's KEM: `dhkem-p256`, `dhkem-p384`,
  `dhkem-x25519`, `mlkem768-x25519`; HKDF-SHA256/384 and AES-256-GCM.
- Held as `secure.Secret` (go-enclave sealed) per holder, with a timer that
  wipes it at `CeremonyConfig.ShareTTL` (default `ceremony.DefaultShareTTL`,
  24h). Never persisted.
- Claimed once: `claim/begin` issues a 32-byte nonce (5-minute TTL) to a caller
  whose connection presents the enrolled certificate; `claim/complete` checks
  the thumbprint again, verifies the signature over the raw nonce bytes with
  the certificate key, returns the envelope and destroys the held copy.
- Opened offline by the holder; opening needs no server.

## Quorum rounds

A round is keyed by (domain, op) and opened on demand for every op except
`rekey`, which only `StartRekey` opens. The round pins the roster digest in
force when it opens; a round whose roster has since been replaced is
discarded. A submission, and `StartRekey`, read the roster before they take
the quorum lock. When the open round's digest differs from the one read, the
roster is re-read under the lock:

- If the caller's read is no longer current, the caller is the stale one. It
  is refused with `*seal.StaleRosterError` (Aborted, retryable), and the newer
  round keeps the shares already counted.
- Only a round that is itself stale is replaced. Admission (`admitLocked`) requires, in order:

1. a holder verifier is installed (`ErrHolderVerifierNotConfigured`);
2. the op is valid in the domain and the barrier's state
   (`QuorumOpInvalidError`);
3. the holder is in the roster (`HolderNotInRosterError`) and not being removed
   (`HolderRevokedError`);
4. the holder has not contributed (`HolderAlreadySubmittedError`);
5. the verifier accepts the proof (`HolderAuthenticationError`,
   `FactorNotAdmittedError`) — the challenge is consumed either way;
6. the share's x-coordinate is the holder's share index
   (`ShareIndexMismatchError`).

At M the round is detached, the key is combined and must open the domain's
blob (`ErrUnsealKeyMismatch` otherwise), and the op's finisher runs with the
authenticated root key, which it must not retain. Every accumulated share is
destroyed when the round completes, fails or expires (`seal.share_ttl`,
default 15m, `ErrShamirQuorumExpired`).

## Holder verification

`VerifyHolder` resolves the challenge by id, checks it was issued for exactly
this domain, op, holder and roster digest (`ErrQuorumChallengeMismatch`), and
verifies the factor response with the registered `authfactors.Factor` against
the **roster's** copy of the factor's public material, not the registry's.
Each challenge expires on the factor's own deadline.

### Challenge issuance

The sealed window admits a challenge request anonymously, so `BeginQuorum` is
hardened against a caller who is not the holder:

- **Throttled per caller.** Every request passes
  `HolderDeps.QuorumBeginLimiter` before anything else, including a request
  for an unknown holder. The key names the scope instance and the caller: an
  authenticated identity, else the certificate the TLS connection presented,
  else the scope's single anonymous bucket. The node's composer installs the
  shared `pkg/ratelimit` engine:
  - each identified caller gets 2 requests per second, burst 10;
  - the anonymous bucket gets 5 per second, burst 20.

  A refusal is `*ceremony.QuorumBeginRateLimitedError`, carrying the retry
  delay; REST sends it as `Retry-After`. An embedder that builds a ceremony
  service by hand and installs no limiter gets no throttling.
- **No holder enumeration.** A name outside the roster, or a holder asked for
  a factor the policy does not admit, gets a decoy challenge. A decoy is
  shaped like an mtls challenge to a real holder: a fresh nonce, the public
  key and thumbprint of a key generated for it, and the same lifetime. It is
  never stored, so a submission against it fails with
  `ErrQuorumChallengeNotFound`, as an expired or evicted challenge does. The
  decoy names the requested factor, or mtls when none was requested. A holder
  whose designated factor is not mtls, asked for without a factor, therefore
  answers with that factor's name.
- **Bounded per holder and operation.** A holder keeps at most 2 pending
  challenges per operation, and a further request evicts the oldest. A
  challenge requested over a connection presenting the holder's own enrolled
  certificate is *bound*. A request that does not present the holder's
  certificate never evicts a bound challenge. Such a request is refused with
  `ErrTooManyPendingChallenges` when every slot is bound. A holder who
  connects with their certificate therefore cannot be locked out by a caller
  naming them.
- **Bounded per scope.** Challenges issued against a replaced roster are
  dropped, since they can never verify. The table is capped at what the roster
  can use: every holder, every operation of the domain, 2 each, and never
  above 256.

## Rekey and revocation

`StartRekey(domain, RekeyParams{Threshold, Policy, Remove})` validates the
redistribution up front through `PrepareRoster`, so an impossible one is
refused before any holder is asked; a revocation that would leave fewer than M
holders fails with `RevocationBreaksQuorumError`. The round is completed by M
current holders not in `Remove`, and then redistributes. Only one rekey round
per domain may be open (`ErrQuorumRoundInProgress`).

## Invariants

- **Shares never appear in a response** other than a claim, and there only
  sealed to one holder.
- **No API accepts a share except `SubmitHolderShare`**, and it counts a share
  only for an authenticated holder, once, at that holder's own index.
- **The roster is bound to the key.** Its digest is in the wrapped blob's AEAD
  associated data and in every envelope's HPKE info.
- **One person, one share, one role per scope**: usernames, certificate keys
  and envelope keys are unique within a scope, and nobody enrols themselves
  (`ErrSelfEnrolment`).
- **Certificate identity is CA-assigned**; the CSR supplies only a public key.
- **Membership changes need the quorum**: every change of holders, threshold
  or policy is a rekey completed by M current holders.
- **Rekey never rotates the root key** and re-encrypts no data; it revokes
  every earlier share of the domain.
- **Distribution is crash-safe by ordering**: envelopes first, blob last.
- **SO-PIN-gated operations fail closed** when no PIN backend is configured
  (`pin.ErrBackendNotConfigured`); PINs are compared hashed, in constant time,
  by the backend.
- **Transports wipe decoded share bytes** after the service call.
- **A barrier whose wrapped blob predates the roster** answers
  `ErrHolderRosterMissing`; there is no unauthenticated fallback.

## Error reference

| Error | Condition |
|---|---|
| `ErrHolderVerifierNotConfigured` | no verifier installed; every submission refused |
| `ErrHolderRosterMissing` | the domain has no roster-bearing blob |
| `ErrQuorumRoundNotOpen` | a `rekey` submission with no rekey started |
| `ErrQuorumRoundInProgress` | a second rekey started on a domain |
| `ErrShamirQuorumExpired` | the round's TTL elapsed |
| `ErrUnsealKeyMismatch` | the reconstructed key did not open the blob |
| `ErrShamirThresholdInvalid` | threshold < 2 or > total |
| `ErrShamirUnsealRequiresQuorum` | a credential unseal of a Shamir barrier |
| `ErrShamirBarrierRequiresQuorum` | a single-caller seal of a Shamir barrier |
| `ErrCeremonyDistributorNotConfigured` | a distribution with no distributor |
| `ErrRecoveryKeysExist` | recovery generation when a recovery key exists; rekey it |
| `ErrRecoveryKeysNotFound` | no recovery key |
| `HolderNotInRosterError`, `HolderRevokedError`, `HolderAlreadySubmittedError`, `ShareIndexMismatchError`, `FactorNotAdmittedError`, `HolderAuthenticationError` | admission refusals |
| `QuorumOpInvalidError` | op not valid in the domain or state |
| `RevocationBreaksQuorumError`, `HolderCountMismatchError`, `InvalidRosterError` | distribution refusals |
| `ceremony.ErrSelfEnrolment`, `ErrDuplicateUsername`, `ErrDuplicateHolderKey`, `ErrInvalidHolderRole`, `InvalidHolderNameError`, `EncryptionKeyRequiredError`, `FactorNotEnrolledError`, `DuplicateFactorError`, `HolderFactorEnrollmentError` | enrolment refusals |
| `ceremony.ErrShareAlreadyClaimed`, `ErrShareExpired`, `ErrNoPendingShare`, `ErrCertThumbprintMismatch`, `ErrChallengeExpired`, `ErrChallengeVerificationFailed` | claim refusals |
| `ceremony.ErrQuorumChallengeNotFound`, `ErrQuorumChallengeMismatch`, `ErrTooManyPendingChallenges`, `*QuorumBeginRateLimitedError` | challenge refusals |
| `ceremony.ErrShareEnvelopeMalformed`, `ShareEnvelopeError` | envelope parse/open failures |

Every one is classified by the `holderRules` block of `pkg/api/errclass`;
see
[api.md](api.md#error-classes).

## Configuration

| Key | Env | Default | Meaning |
|---|---|---|---|
| `seal.share_ttl` | `QRDB_SEAL_SHARE_TTL` | `15m` | how long a quorum round stays open |

The envelope time-to-live is `CeremonyConfig.ShareTTL` (24h). Threshold, total
and policy are per distribution, not configuration.

## Persistence

- **Wrapped blobs**: `<RootKeyPath>.wrapped` (unseal) and
  `<RootKeyPath>.recovery.wrapped` (recovery), version 2, each carrying its
  roster.
- **Holder records**: `ceremony/officers/` behind the barrier.
- **Envelopes, rounds, challenges**: memory only.
- **Shares**: nowhere.

## Test strategy

- **Unit** (`pkg/seal`, `pkg/ceremony`): roster validation and tamper
  detection, each admission refusal, factor policies, round expiry and
  replacement, rekey and revocation refusal, envelope binding, one-time claim,
  share TTL. Zeroization is asserted, not assumed.
- **Transport** (`pkg/api/{rest,grpc,quic,mcp,unix}`, embedded): every holder
  route, its authorization, its error classes, and that submitted share bytes
  are wiped.
- **Conformance** (`sdk/go/conformance/share_holders_test.go`): on every SDK
  transport, an administrator enrols two Security Officers and three
  custodians (P-256, P-384, X25519 and ML-KEM hybrid envelope keys; `mtls`
  and an assertion factor) at 3-of-5; holders claim; the node restarts sealed
  and is unsealed through the sealed window by three holders with different
  factors; a duplicate submission, a non-designated factor and a revoked
  holder are refused; revocation and rekey need the quorum; a rekey to
  `any-enrolled` admits a holder's other factor.
- No `time.Sleep`: expiry is driven through the clock seam.

## Observability

The barrier emits audit events `distribute`, `rekey-start`, `quorum-share`
(allow / deny, with the holder and op) and `quorum-complete`, alongside
`initialize`, `unseal` and `seal`. The ceremony
service logs enrolment (holder, role, designated factor, KEM) and claims.
Holder status is available from the list operation and never includes a share.
