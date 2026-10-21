# Shamir Custody — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21

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
`HolderVerifier` (`AdmitSubmission`, `VerifyHolder`) are consumer-side interfaces; `pkg/ceremony`
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
func (s *CeremonyService) StartRekey(ctx, domain, p seal.RekeyParams) (*seal.QuorumRoundInfo, error)
func (s *CeremonyService) InitializeShamir(ctx, policy seal.FactorPolicy) (*seal.ShamirInitParams, error)
func (s *CeremonyService) GenerateRecoveryKeys(ctx, threshold int, policy seal.FactorPolicy) (*seal.RecoveryKeyResult, error)
func (s *CeremonyService) SetAuditLogger(l audit.AuditLogger)
func (t *ScopedCeremonyService) StartRekey(ctx, barrier *seal.StorageBarrier, p seal.RekeyParams) (*seal.QuorumRoundInfo, error)
func (s *CeremonyService) Rebind(barrier *seal.StorageBarrier)
func ParseShareEnvelope(raw []byte) (*ShareEnvelope, error)
func (e *ShareEnvelope) Open(dec keydelivery.Decrypter) ([]byte, error)
func (e *ShareEnvelope) OpenWithKey(priv crypto.PrivateKey) ([]byte, error)
```

`Open` takes a `keydelivery.Decrypter` (an `hpke.PrivateKey`), not a key
value. A holder whose envelope key is held on a device builds the decrypter
with `keydelivery.NewDecrypter(kem, kx, pq)`: `kx` is an `ecdh.KeyExchanger`
and `pq`, for the hybrid suite, a `crypto.Decapsulator`, each of which sends
its operation to the device. The private key is never in the process.
`OpenWithKey` is the software path for a key file.

Composition (Tier 2), through the server and node options:
`WithCeremonyCA`, `WithFactorRegistry` (hardware factors from go-xkms),
`WithPINBackend`. `sdk/go/node` wires the ceremony service as the barrier's
distributor and verifier, for the sealed window and the full node alike.

`sdk/go` — `transport.BarrierClient` and `transport.TenantClient` carry the
holder methods listed in [api.md](api.md); `sdk/go/transport/embedded`
serves them in-process.

Every rekey of a holder set -- a revocation included -- is opened through its
holder authority's `StartRekey`, never on the barrier directly, so the opening
is on the compliance trail. `HolderDeps.Audit` is that trail.

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

**Before initialization a database barrier takes at most N holders.** A
database barrier's M-of-N is fixed at its create, and its initialization
splits to exactly that N, so registration past N on a database barrier not
yet initialized is refused with `*ceremony.HolderLimitReachedError`
(`FAILED_PRECONDITION`). No holder can be revoked before initialization, so an
N+1th holder -- from a second browser tab, the database view beside the
wizard, or the CLI -- would leave the database with no way forward. Once the
barrier is initialized a new holder enrols freely and joins the holder set at
the next rekey. No other scope fixes N beforehand: the system barrier's and a
tenant's initialization take the M-of-N with the request, and a recovery key
is split to every recovery holder enrolled.

**A database holder is unenrolled before initialization.** No share exists
before a database barrier is initialized, so a holder enrolled by mistake, or
one whose key is lost before it was ever used, is removed without a quorum
(`ScopedCeremonyService.UnenrolOfficer`, the database service's
`DatabaseBarrierOfficerUnenrol`, on every transport); its place is free for
another holder. On an initialized barrier every holder may hold a share and
unenrolment is refused with `ceremony.ErrUnenrolInitialized`: removal is a
revocation, a rekey the remaining holders complete. Every Shamir barrier is
initialized through its holder authority -- a database's and a tenant's
through `ScopedCeremonyService.InitializeShamir` (the tenant service's Shamir
initialization included), the system barrier's through
`CeremonyService.InitializeShamir` (the ceremony's initialization and
`BarrierService.BarrierInitializeShamir` included) -- which marks the
initialization in flight: the barrier reads the holder set twice -- to validate it, and after
generating the key, once it already reads as initialized, to split it -- and
a registration, an unenrolment or a second initialization between the two is
refused with `ceremony.ErrInitializationInProgress` (`ABORTED`; retry once it
finishes). Admitted, a registration there would be dealt a share the key was
never sized for and fail the split, and an unenrolment would leave the
barrier initialized with no share set. The recovery domain's initialization
is held to the same rule: the recovery key is generated through
`CeremonyService.GenerateRecoveryKeys` (`BarrierService.BarrierGenerateRecoveryKeys`
included), which marks the recovery scope's initialization in flight on the
same mark, so a recovery holder registered or unenrolled between the roster
read and the sealing of the shares -- or a second generation -- is refused
with `ceremony.ErrInitializationInProgress`. The removal
is recorded (`holder-unenrol`) before the holder is removed and, under the
node's `fail-closed` audit posture, refused when the trail cannot record it. The engine verb serves
any scope; only the database scope exposes it, the one whose N is fixed
before initialization and so the one an enrolment can leave stuck.

The reconciliation on the first unsealed access never overwrites a persisted
holder:

- It writes a cached record only when storage has no holder of that name.
- A cached record with the same identity (certificate, signing key and
  envelope key) as the persisted one leaves the persisted record standing.
  Only a claim recorded while sealed, for the same distribution, is carried
  onto it.
- A cached record with a different identity is a conflict
  (`*ceremony.HolderRecordConflictError`). The cached record is evicted, the
  persisted holder kept, and the conflict recorded on the barrier's audit log
  (`holder-record-conflict`, `deny`, with the scope, username and the cached
  certificate thumbprint) and the service log. The reconciliation then carries
  on with the other cached holders. It does not fail: a failed reconciliation
  is retried by every later holder operation, so one conflicting registration
  would otherwise wedge every holder operation of the scope.

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
   (`HolderCountMismatchError`). A holder whose record shows an earlier
   distribution but who is absent from the roster in force was removed. Such
   a holder is excluded even if deleting their record failed (see step 4). A
   holder enrolled since the last distribution joins.
2. A fresh 256-bit domain key is generated straight into protected memory
   (`secure.Buffer`). It wraps the root key (AES-256-GCM) with associated data
   `go-qrdb/seal/wrapped-root-key/v2|<RootKeyPath>|<domain>|<roster digest>`,
   and the blob stores the roster bytes in clear.
3. `Distribute` is handed the key buffer and borrows it only to split it. It
   HPKE-seals share *i* to the holder with share index *i*, into a **staged**
   set. The envelope header — version, KEM, scope, domain, holder, share
   index, threshold, total, distribution id and roster digest — is bound into
   the HPKE info, so an edited header makes the envelope unopenable. The
   envelopes in force, unclaimed ones included, are untouched. A staged set
   that never committed is discarded by the next `Distribute`.
4. The blob is persisted. `Committed` then swaps the staged envelopes in,
   destroying the previous set. Only after that does it record share indexes
   and delete removed holders' records. The domain key buffer is destroyed
   when the distribution returns. When nothing is staged for the committed
   roster's distribution, `Committed` refuses with
   `ceremony.StagedDistributionMissingError` and changes nothing.

Redistributions of one domain are serialised on the barrier (a one-place slot
per domain; a caller whose context ends while waiting gets the context's
error). The distributor stages one set per domain, so a second distribution
interleaved with the first would discard the first's staged envelopes, and
the first would then persist a blob no envelope in force opens.

A failure before the blob is persisted leaves the previous blob in force,
with the previous share set and every unclaimed envelope of it; the
operation can be re-run. The envelopes in force always belong to the blob in
force. If `Committed` fails after the swap, a removed holder's record may
survive, but step 1 never deals that holder a share again.

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
  the thumbprint again, verifies the certificate key's signature over the
  share-claim message, returns the envelope and destroys the held copy. The
  message (`ceremony.ShareClaimMessage`) is `qrdb/share-claim/v1`, the scope's
  claim label (`BarrierScope.ClaimLabel`), the username and the nonce,
  NUL-separated. The `mtls` quorum factor signs `mtls.ChallengeMessage`
  instead (see Holder verification), so the two signatures are
  domain-separated and neither verifies as the other.
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
3. the verifier admits the submission at all (`HolderVerifier.AdmitSubmission`):
   pkg/ceremony throttles it per caller with the policy its challenge
   requests have, in a submission bucket of its own, and the refusal
   (`*ceremony.QuorumRateLimitedError`) is returned before anything in the
   submission is looked at;
4. the verifier accepts the proof (`HolderAuthenticationError`, wrapping e.g.
   `ErrQuorumChallengeNotFound` or `FactorNotAdmittedError`) — the challenge is
   consumed either way. This comes **before** any check that would say
   something about the holder or the round, so a stranger's name, a holder
   being revoked and a holder who has already submitted all answer an
   unverified proof exactly as any holder does;
5. only then, the holder is in the roster (`HolderNotInRosterError`), not
   being removed (`HolderRevokedError`), and has not contributed
   (`HolderAlreadySubmittedError`);
6. the share's x-coordinate is the holder's share index
   (`ShareIndexMismatchError`).

At M the round is detached, the key is combined and must open the domain's
blob (`ErrUnsealKeyMismatch` otherwise), and the op's finisher runs with the
authenticated root key, which it must not retain.

A share is custodied from the moment the round accepts it to the moment the
round ends, and the round has no exit that leaves it behind.
`shamir.NewProtectedShare` (go-quicraft, over go-enclave) moves the share
into a locked, guard-paged enclave and wipes the value it was given;
`SubmitHolderShare` wipes `HolderSubmission.Share` before it returns, whatever
the outcome, refusals included. The x-coordinate check decodes four base64
characters into a stack buffer that is wiped. `shamir.CombineProtected`
reconstructs into an enclave, and the key is copied out only into a
`secure.Buffer` that is destroyed on return. Every accumulated share is
destroyed when the round:

| Exit | Where |
|---|---|
| completes, or fails to reconstruct or to open the blob | `submitAgainst`, `finishRound` |
| expires (`seal.share_ttl`, default 15m, `ErrShamirQuorumExpired`) | the round's expiry timer |
| is replaced: its roster changed, its TTL lapsed before its timer fired, or an expired rekey round is reopened | `roundLocked`, `StartRekey` |
| loses its share set: the domain is redistributed (rekey, revocation, recovery-key generation) or the recovery keys are deleted | `redistributeTo`, `DeleteRecoveryKeys` |
| can no longer run: a quorum unseals the barrier, which ends every unseal and recovery round | `unsealFromRootKey` |
| outlives the session: the barrier is sealed or closed | `Seal`, `Close` |

Expiry is eager: opening a round arms a timer for its TTL, and the timer
destroys the round's share buffers whether or not another share ever arrives.
A round that completes or is replaced stops its timer, and a timer never
touches a round that has left the open set. `Seal` and `Close` destroy every
open round (`Close` does so even on an already sealed barrier, which is when an
unseal round holds shares). Seal and unseal change the barrier's state before
they sweep, and admission re-checks the state under the round lock, so a
submission racing the change cannot open a round behind the sweep. The rounds'
time source is `StorageBarrier.SetClock`, so tests expire a round without
sleeping.

There is no operation that cancels an open round: a round ends by one of the
exits above, and the TTL bounds how long an abandoned one holds shares.

## Holder verification

`VerifyHolder` resolves the challenge by id, checks it was issued for exactly
this domain, op, holder and roster digest (`ErrQuorumChallengeMismatch`), and
verifies the factor response with the registered `authfactors.Factor` against
the **roster's** copy of the factor's public material, not the registry's.
Each challenge expires on the factor's own deadline.

Every quorum challenge is bound to its purpose: `BeginQuorum` records
`quorum:<claim label>:<domain>:<op>` (for example `quorum:system:unseal:seal`
or `quorum:tenant:acme:unseal:rekey`) in the challenge's method data under
`purpose` (`mtls.MethodDataPurpose`), decoys included. The `mtls` factor's
answer is a signature over `mtls.ChallengeMessage`:
`qrdb/mtls-challenge/v1`, the challenge ID, the purpose and the nonce,
NUL-separated (`mtls.SignChallenge`). It is verified against the
**server-held** challenge, so a client cannot change the purpose, and a
signature over the bare nonce, or one made for another challenge, scope,
domain or operation, does not verify.

### Challenge issuance

The sealed window admits a challenge request anonymously, so `BeginQuorum` is
hardened against a caller who is not the holder:

- **Throttled per caller.** Every request -- a challenge request, and a share
  submission through `AdmitSubmission` -- passes `HolderDeps.QuorumLimiter`
  before anything else, including a request for an unknown holder. Each kind
  has a bucket of its own per caller: a holder answers every challenge with
  one submission, so a shared bucket would halve the quorums the anonymous
  bucket can serve. The key names the scope instance and the caller: an
  authenticated identity, else the certificate the TLS connection presented,
  else the scope's single anonymous bucket. A certificate keys the caller
  only when the TLS stack verified it (`VerifiedChains` non-empty); an
  unverified certificate is anyone's to present, so its caller is anonymous.
  Every anonymous caller of a scope shares one bucket, which is a deliberate
  trade-off: a flood of anonymous requests can starve another anonymous
  caller, a holder included, of challenges until the bucket refills. A holder
  who connects with their verified certificate, or authenticates, has a
  bucket of their own and is never starved by anonymous traffic. The node's
  composer installs the
  shared `pkg/ratelimit` engine:
  - each identified caller gets 2 requests per second, burst 10;
  - the anonymous bucket gets 5 per second, burst 20.

  A refusal is `*ceremony.QuorumRateLimitedError`, carrying the retry
  delay; REST sends it as `Retry-After`, gRPC and the Unix socket as a
  `google.rpc.RetryInfo` status detail, MCP as `retry_after_seconds` in the
  error envelope, QUIC as a `google.rpc.RetryInfo` detail in the error
  frame's payload (`errclass.RetryAfter` decides, once, which refusals carry
  one). An embedder that builds a ceremony
  service by hand and installs no limiter gets no throttling.
- **No holder enumeration.** A name outside the roster, or a holder asked for
  a factor the policy does not admit, gets a decoy challenge, issued by the
  requested factor's own `Challenge` from a decoy enrollment the factor
  derives (`authfactors.DecoyEnroller`), so it has exactly that factor's
  shape. A real holder's public material is the same on every request, so a
  decoy's is too: it is derived from the domain's decoy seed and the name.
  For mtls the thumbprint is HMAC-SHA256(seed, name) and the key scalar
  HKDF-SHA256(seed, name, kind), on the key type the roster's holders
  predominantly use (P-256, P-384, P-521 or Ed25519). The seed is persisted
  with the roster metadata in the barrier's base storage
  (`StorageBarrier.DecoySeed`), so decoys survive a restart. Every request
  derives the decoy for the name it asks about, holder or not, so a real and
  a decoy answer cost the same work.

  A decoy is **stored** like a real challenge, in a separate bounded table
  (256, oldest evicted) that can never crowd a holder's challenges out, so a
  submission against it runs the same checks and the same factor `Verify`,
  and fails as a wrong answer to a real challenge fails. The refusal itself
  is opaque: `*seal.HolderAuthenticationError` has one fixed message
  ("share holder authentication failed"), names no holder and does not
  unwrap to the verifier's reason, which goes only to the server log and the
  audit record (`reason`). A begin followed by a bogus submit therefore
  answers a holder and a stranger with the same text and class. A factor
  that does not implement `authfactors.DecoyEnroller` is answered for with an
  mtls decoy advertised under its name, so its holders are distinguishable
  from strangers by shape; every factor should implement it. The
  decoy names the requested factor, or mtls when none was requested. A holder
  whose designated factor is not mtls, asked for without a factor, therefore
  answers with that factor's name. RSA holder keys are refused at enrolment
  (`ceremony.UnsupportedHolderKeyError`): no RSA key can be derived per name
  at a cost that does not itself reveal the decoy, so an RSA holder would be
  told apart from a stranger by the key type.
- **Bounded per holder and operation.** A holder keeps at most 2 pending
  challenges per operation, and a further request evicts the oldest. A
  challenge requested over a connection presenting the holder's own enrolled
  certificate is *bound*. A request that does not present the holder's
  certificate never evicts a bound challenge. When every slot is bound, such
  a request is answered with its challenge all the same, kept in the decoy
  table rather than the holder's slots, so it evicts nothing; it is not
  refused, because a refusal is an answer no stranger's name could produce.
  The same holds when the scope's table is full. A holder who
  connects with their certificate therefore cannot be locked out by a caller
  naming them.
- **Bounded per scope.** Challenges issued against a replaced roster are
  dropped, since they can never verify. The table is capped at what the roster
  can use: every holder, every operation of the domain, 2 each, and never
  above 256.

### Anonymous claim throttle (embedded transport)

`claim/begin` and `claim/complete` carry no equivalent of `QuorumLimiter`
inside `pkg/ceremony` itself -- a claim with an identity on its context is
rate-limited by `pkg/auth/middleware` instead, exactly like any other
authenticated request. On a network transport that is always true: the TLS
connection authenticates to some identity before the handler runs. On the
embedded transport, composed with no `AuthMiddleware`, a claim reaches
`claimBegin`/`claimComplete` (and the tenant and database equivalents) with
no identity at all, so without a throttle of its own a caller could probe
holder usernames and roster entries in-process without limit.

`sdk/go/transport/embedded`:

```go
type HolderActLimitFunc func(key string, anonymous bool) (allowed bool, retryAfter time.Duration)

func WithHolderActLimiter(fn HolderActLimitFunc) Option

type HolderActRateLimitedError struct {
	Operation  string
	RetryAfter time.Duration
}
```

`limitHolderAct` is a no-op when no limiter is installed, or when
`auth.IdentityFromContext` finds an identity on the call's context --  a
network-transport parity check that never re-charges a call the auth
middleware already rate-limited. Otherwise it charges
`"embedded-holder-claim\x00" + <scope key>` and, over the limiter's refusal,
returns `*HolderActRateLimitedError` before the service is called at all.
The scope key is `barrierScopeKey` (system unseal/recovery),
`tenantScopeKey` or `databaseScopeKey` -- one bucket per barrier, shared by
every anonymous caller of it, with the same tenant-normalization rule as the
rate-limit package's own key (`tenantid.Normalize`): a claim naming `""` and
one naming the system tenant are the same barrier and must not be split
across two buckets.

`sdk/go/node` installs `lifecycle.QuorumLimit` -- the SAME engine and
anonymous policy (5/s, burst 20) `HolderDeps.QuorumLimiter` applies to quorum
challenges above -- wrapped in a closure that prefixes every key with a
16-byte random hex identifier generated once per node (`newHolderActLimit`),
so two nodes sharing one process, or two ceremonies re-created in tests,
never share a bucket despite sharing the underlying engine.

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
- **Certificate identity is CA-assigned**; the CSR supplies only a public key,
  which must be ECDSA P-256, P-384, P-521 or Ed25519. An RSA key is refused
  with `ceremony.UnsupportedHolderKeyError` (`INVALID_ARGUMENT`).
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
| `HolderNotInRosterError`, `HolderRevokedError`, `HolderAlreadySubmittedError`, `ShareIndexMismatchError`, `FactorNotAdmittedError`, `HolderAuthenticationError` | admission refusals |
| `QuorumOpInvalidError` | op not valid in the domain or state |
| `RevocationBreaksQuorumError`, `HolderCountMismatchError`, `InvalidRosterError` | distribution refusals |
| `ceremony.HolderLimitReachedError` | registration past a database barrier's N before initialization |
| `ceremony.ErrUnenrolInitialized` | unenrolment once the barrier is initialized; revoke instead |
| `ceremony.ErrInitializationInProgress` | a registration, an unenrolment or a second initialization while one is in flight; retry |
| `ceremony.ErrSelfEnrolment`, `ErrDuplicateUsername`, `ErrDuplicateHolderKey`, `ErrInvalidHolderRole`, `InvalidHolderNameError`, `EncryptionKeyRequiredError`, `FactorNotEnrolledError`, `DuplicateFactorError`, `HolderFactorEnrollmentError` | enrolment refusals |
| `ceremony.ErrShareAlreadyClaimed`, `ErrShareExpired`, `ErrNoPendingShare`, `ErrCertThumbprintMismatch`, `ErrChallengeExpired`, `ErrChallengeVerificationFailed` | claim refusals |
| `ceremony.ErrQuorumChallengeNotFound`, `ErrQuorumChallengeMismatch`, `*QuorumRateLimitedError` | challenge refusals (`ErrTooManyPendingChallenges` is internal: a request it would refuse gets a challenge stored in the decoy table, which verifies when answered) |
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

**Integration target:** `make integration-test-shamir-custody` (no Docker; selection in `test/integration/features.mk`). It runs `barrier_cli_test.go`, `tenant_barrier_officer_cli_test.go`, `tenant_barrier_shamir_cluster_cli_test.go` and `operator_tls_holder_cli_test.go`; the rest of the system barrier's holder flows run under `make integration-test-barrier-seal` and `make integration-test-ceremony`. Per-transport accounting is the `shamir-custody` row of `test/integration/COVERAGE.md`.

- **Unit** (`pkg/seal`, `pkg/ceremony`): roster validation and tamper
  detection, each admission refusal, factor policies, round expiry (swept eagerly,
  with no further share) and replacement, destruction of every round's shares
  on each exit in the table above (`quorum_sweep_test.go`,
  `quorum_custody_test.go`, `quorum_replace_test.go`, asserted through the protected share's destroyed
  state) and of the submitted plain buffer on every outcome, rekey and
  revocation refusal, envelope binding, one-time claim,
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
- **Audit** (`pkg/ceremony/holder_audit_test.go`): each scope -- system,
  recovery, tenant and database -- through every holder event and its refusal,
  keyed by the owning tenant, with the trail searched for every share,
  envelope, nonce, signature, factor response and holder key the flow
  handled; a refused record leaves the operation's outcome unchanged. The
  tenant, database and barrier services' rekey and revocation verbs are
  pinned to open their rounds through the authority. `cmd/qrdbd` pins that
  the trail is open in the sealed window, that start fails before the window
  when it cannot be opened, and that an unseal quorum gathered there is
  recorded.
- No `time.Sleep`: quorum round expiry is driven through the clock seam.
  `StorageBarrier.SetClock` installs the clock rounds are dated and expired
  on (the real clock by default), and the expiry tests advance a
  `clocktest.Fake` past the round TTL.

## Observability

Every holder scope records its holder events on the node's compliance trail
(`HolderDeps.Audit`), keyed by the scope's owning tenant: `holder-register`,
`holder-claim`, `holder-quorum-submit`, `holder-rekey-start`,
`holder-revoked` and `holder-unenrol`, refusals included, with the scope, the
holder and the
operation and never a share, an envelope, a PIN, a signature, a factor
response or a key; a refusal's reason is a fixed token, never an error's
text. A registration, a claim and a rekey opening are recorded before they
take effect and, under the node's `fail-closed` audit posture, refused with
`*audit.GateError` when the trail cannot record them -- a refused claim
leaves the envelope held and claimable. See
[../audit/engineering.md](../audit/engineering.md#audited-events). The daemon opens the trail before the sealed window, so
the claims and unseal quorums served there are recorded; see
[../audit/architecture.md](../audit/architecture.md#the-trail-exists-before-the-sealed-window).

The barrier records its own events -- `distribute`, `quorum-complete`,
`initialize`, `unseal` and `seal` among them -- on the same trail
([../barrier-seal/engineering.md](../barrier-seal/engineering.md#observability)).
Each event has one emitter: a submission is `holder-quorum-submit` and a rekey
opening `holder-rekey-start`, from the holder engine. The ceremony service logs enrolment (holder, role,
designated factor, KEM) and claims.
Holder status is available from the list operation and never includes a share.
