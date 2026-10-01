# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html).

Pre-1.0, MINOR is where breaking changes live; PATCH is reserved for changes
that do not move the API. Releases before `0.2.0-alpha` predate this file and
are not reconstructed here — their history is in the git log.

## [Unreleased]

### Added

- **The embedded-server wizard enrols share holders before a Shamir
  initialization.** Its Shamir mode called `initializeShamir` with no holder
  enrolled, which the server refuses. It now starts the embedded server,
  pauses for the operator to enrol exactly the configured number of holders
  (username, role, CSR; the issued certificate is shown to hand back), and
  only then initializes. The enrolment forms are one component shared with
  the setup wizard.
- **A frontend CI job.** The `frontend` job (`make test-frontend`) runs the
  TypeScript SDK's type check and vitest suite, svelte-check over the web UI
  and the mocked Playwright tier, and gates `ci-gate`. The Playwright specs
  that encoded the retired custody model are ported to share holders; the
  real-backend tier claims and submits with a headless `qrdb` CLI
  (`QRDB_BIN`, built by `make build-cli-headless`).
- **Re-initializing a barrier to Shamir in the Seal Manager enrols its
  holders first,** as the setup wizards do.
- **Tenant share holders in the web UI.** The Tenant Manager gains a
  Holders panel per tenant: it lists the Shamir tenant barrier's holders with
  role, share index and claim state, and opens rekey and revocation rounds
  (`startTenantRekey`, `revokeTenantOfficer`). The rekey/revoke form is one
  component shared with the Seal Manager's system-barrier panel.
- **Unified share holders.** Security Officers and key custodians are one
  kind of share holder, distinguished only by role (`so` | `custodian`) and
  the RBAC rights that role carries. Both are enrolled the same way — by an
  Admin or a Security Officer, never by themselves — from a CSR whose public
  key is all the CA takes: the ceremony CA assigns `CN=<username>` and a
  SPIFFE URI in the holder namespace
  (`spiffe://<trust-domain>/tenant/<tenant>/holder/<username>`), which the
  authentication layer refuses as a login identity. Enrolment records an
  envelope recipient key (the certificate key, or a separate HPKE key) and
  the holder's authentication factors: `mtls` always, plus any factor the
  composer registered with `WithFactorRegistry`. One person holds one share,
  in one role, per scope. See `docs/shamir-custody/`.
- **Algorithm-agile HPKE (`pkg/keydelivery`).** A fixed suite registry —
  `dhkem-p256`, `dhkem-p384`, `dhkem-x25519` and the post-quantum hybrid
  `mlkem768-x25519` — shared by node key delivery and share envelopes.
- **The `mtls` authentication factor (`pkg/authfactors/mtls`).** Possession
  of the enrolled certificate key, proved by signing a server nonce, so it
  works on every transport; when the caller arrived over a verified TLS
  session, the session certificate must also be the enrolled one.
- **The holder roster.** Each key domain's wrapped-key blob (now version 2)
  carries the public list of its holders, their share indexes and their
  factors' public material, with the roster's SHA-256 digest in the AEAD
  associated data: readable while sealed, and any edit makes the key fail to
  open (`ErrUnsealKeyMismatch`). A factor policy — `designated` (default) or
  `any-enrolled` — is chosen per share set and bound into the roster.
- **One authenticated quorum.** Unseal, seal, root token, rekey completion
  and recovery all run through `StorageBarrier.SubmitHolderShare`: a share
  counts only for a holder in the roster, not being revoked, not already
  counted in the round, whose factor proof verifies against the roster over
  a challenge issued for exactly that round, and whose share carries its own
  index. Served on every transport as `quorum begin` / `quorum submit`
  (REST `POST /v1/barrier/quorum/{begin,submit}`, gRPC/Unix
  `BarrierQuorumBegin`/`BarrierQuorumSubmit`, QUIC `barrier.QuorumBegin`/
  `barrier.QuorumSubmit`, MCP `qrdb_barrier_quorum_begin`/`_submit`,
  embedded), reachable through the sealed window, with tenant equivalents.
- **Quorum-gated rekey and holder revocation.** `BarrierRekeyStart` /
  `BarrierOfficerRevoke` (REST `POST /v1/barrier/rekey/start`,
  `POST /v1/barrier/officers/revoke`; permissions `seal:rekey` and the new
  `seal:officer_revoke`, plus the SO PIN) open a round that completes only
  when M current holders submit; revocation removes the holder, is completed
  by M of the remaining holders, and is refused when M would exceed N−1.
  Tenant forms: `tenant_barrier:rekey`, `tenant_barrier:officer_revoke`.
- **An independent recovery key.** Recovery generation wraps the root key
  under its own key with its own blob and roster, split to the recovery
  holders; it unseals with `op: recover` and is rekeyed on its own.
- **Holder CLI and desktop verbs.** `qrdb barrier officer
  register|list|claim|claim-begin|claim-complete|revoke`, `qrdb barrier quorum
  begin|submit`, `qrdb barrier rekey`, their `qrdb tenant barrier-officer`,
  `barrier-quorum`, `barrier-rekey` forms, and `qrdb init --custodians
  --policy`. The desktop `OfficerBinding` offers `RegisterHolder`,
  `ListHolders`, `ClaimEnvelope`, `SubmitShare`, `StartRekey`,
  `RevokeHolder`; keys stay in the calling process.
- **Share-holder errors in the one classification table.**
  `pkg/api/errclass` classifies every holder, quorum and envelope error, so
  the same failure answers the same way on REST, gRPC, Unix, QUIC, MCP and
  embedded.
- **Test support:** `pkg/seal/sealtest.Custody` (in-memory distributor and
  verifier for code that sits on a Shamir barrier) and `pkg/pin/pintest`.

### Security

- **Share submissions are authenticated and counted once per holder.** The
  unauthenticated string-share unseal, seal-vote, root-token and recovery
  entry points are gone; one person who has seen M shares can no longer act
  as M people.
- **Shares reach only their holder.** Each share is HPKE-sealed to its
  holder's enrolled key, held only in go-enclave protected memory with a
  time-to-live, delivered once to the holder's own mTLS session and wiped.
  No response other than the claim carries share material, and the claim
  carries it sealed.
- **The CA assigns holder identities.** A CSR can no longer choose its own
  subject or SANs.
- **SO PIN checks are hashed and constant-time, and fail closed.** Every SO
  PIN comparison goes through the `pin.PINBackend`; raw PINs are never
  compared with `!=`. Every SO-PIN-gated operation is refused without a PIN
  backend (REST 412, `FAILED_PRECONDITION` elsewhere).
- **Transports wipe the share bytes they decode** from a quorum submission.
- **A Shamir barrier can no longer be initialized from a credential.**
  `Initialize`/`InitializeWithStrategy` on a Shamir barrier wrote an envelope
  with no wrapped key and no roster, which could never be unsealed once
  sealed; they now refuse with `seal.ErrShamirInitializeRequiresHolders`.
  That covers `bootstrap.InitializeTenantBarrier` on a pre-registered Shamir
  tenant barrier and `sdk/go/node`'s `Node.Unseal` on an uninitialized one,
  and `qrdb barrier init --barrier-strategy shamir` is refused locally.
- **X25519 envelope suites are refused in FIPS mode.** `dhkem-x25519` and
  `mlkem768-x25519` are not FIPS 140-3 approved; in FIPS mode
  `keydelivery.LookupSuite` refuses them with `SuiteNotApprovedError`, so
  such a holder is refused at enrolment rather than at sealing.
- **Membership changes need the quorum.** Adding, removing or re-policing
  holders is a rekey, and a rekey completes only with M current holders.
- **A system-scope encryption migration to Shamir no longer loses the share
  envelopes.** The in-process data-plane restart it triggers built a new
  ceremony service and discarded the envelopes the migration had just
  distributed, so the barrier came back sealed with no holder able to open it
  and every datum on the node was unreachable. The restart now carries the
  running ceremony service across (`ceremony.CeremonyService.Rebind` moves it
  to the rebuilt barrier), in the daemon and in `sdk/go/node`, so the holders
  claim from the sealed node and a quorum unseals it.
- **A Shamir initialization whose share distribution fails is rolled back.**
  The barrier used to stay initialized and unsealed with a root key no holder
  had a share of, and every retry was refused as already initialized.
  `InitializeShamir` now discards the key, seals, and deletes the persisted
  root key, wrapped key and metadata, so storage and status read as
  uninitialized. The epoch barrier cannot leave the initialized state within
  the process, so a retry needs a node restart; until then initialization
  answers the new `seal.ErrRestartRequired` (FailedPrecondition). A rollback
  that fails returns `seal.ShamirInitRollbackError` (Internal).
- **The REST sealed window no longer admits holder revocation.** The claim
  routes were allowlisted by an `/v1/barrier/officers/` prefix, which also let
  `POST /v1/barrier/officers/revoke` through while the barrier was sealed. The
  allowlist now names the claim routes as one-segment patterns
  (`/v1/barrier/officers/{username}/claim/begin` and `.../complete`, and the
  recovery forms), so a sealed node answers the revocation 503.
- **A distributed Shamir share is destroyed if it is not claimed in time.**
  Every envelope held for a share holder now carries its own expiry timer
  (`CeremonyConfig.ShareTTL`, default `ceremony.DefaultShareTTL` = 24h) and is
  wiped at its deadline. A late claim is refused with the new
  `ceremony.ErrShareExpired` (`sdk/go.ErrCeremonyShareExpired`) at claim
  begin and complete, in the Gone class (REST 410, gRPC/Unix
  `FAILED_PRECONDITION`); the remedy is a rekey. Replacing a held share now destroys the old enclave
  instead of dropping it for the collector.
- **The barrier root key is decrypted straight into protected memory.**
  Unwrapping the Shamir-wrapped root key (unseal, root-token verification)
  now opens the AEAD in place into a `pkg/secure` buffer and hands it to the
  recovery strategy without an intermediate heap slice; an AEAD that returns
  memory it allocated itself is refused with the new
  `seal.UnprotectedDecryptError`.

### Fixed

- **The transactions docs said a branch could carry get and range ops.** The
  code has always refused them (`UnsupportedTransactionOpError`, 400 /
  `INVALID_ARGUMENT`) rather than risk a read defaulting to a write; the
  documents now say so, and their examples use put and delete.
- **Real-backend Playwright specs** that had drifted from the server: barrier
  secrets are sent as base64 (`[]byte` fields), a created user carries the
  email that is its login key, and the transaction spec pins the get-op
  refusal.
- A reopened keyed barrier (`ReopenKeyed`) kept neither the holder verifier
  nor the quorum TTL, so no holder could unseal it after an encryption
  migration.
- `sdk/go/node` did not hand the ceremony CA to the ceremony service, and the
  full boot discarded the sealed-window ceremony state, losing envelopes not
  yet claimed; the sealed window also ran without the PIN backend.
- The embedded transport's `CeremonyInitialize` dropped each holder's role,
  the factor policy and the returned holder certificates.
- `qrdb init --shares` was checked only for range; the key was silently split
  to however many holders were named. It must now equal the number of
  `--officers` plus `--custodians`.
- `seal.ErrShamirUnsealRequiresQuorum` fell through to REST 500 and gRPC
  `INTERNAL`; it now maps like `ErrShamirBarrierRequiresQuorum` (422,
  `FAILED_PRECONDITION`). Ceremony initialization errors (`ErrNoOfficers`,
  `ErrInvalidThreshold`, `ErrAlreadyInitialized`) are now classified instead
  of answering 500.
- The QUIC transport classifies claim errors like every other transport.
- The sealed-window allowlist named a `shamir-verify` verb no transport
  served.

### Removed — BREAKING

- **The parallel custodian subsystem is deleted**: `pkg/custodian`,
  `ceremony.CeremonyExtension`, `CeremonyEnrollExternalOfficer`, the
  custodian-group service on every transport, and the ceremony's own share
  claims. Custodians are share holders with `--role custodian`.
- **The empty Custodian RBAC role**: `role.RoleNameCustodian` and the SDK's
  `RoleNameCustodian` are removed, and `role.BuiltInRoles` returns five roles
  (SO, Admin, Operator, User, Auditor). The role granted nothing; a key
  custodian is a share holder, not an RBAC principal. A tenant seeded by an
  earlier build keeps its stored `custodian` role record, which grants nothing
  and is no longer a reserved system role name, so any user still assigned it
  keeps resolving; remove the assignment and the role at leisure.
- **Batch and unauthenticated share entry points**: `UnsealWithShare(s)`,
  `SubmitSealVote`, `GenerateRootToken`, `RecoverWithKeys`, the one-call
  `Rekey`, and the `ShareExchangeCoordinator` skeleton (never wired; nodes do
  not exchange shares).
- **REST routes**: `/v1/barrier/shamir/unseal-share`,
  `/v1/barrier/shamir/unseal-shares`, `/v1/barrier/shamir/seal-vote`,
  `/v1/barrier/rekey`, `/v1/barrier/recovery/recover`,
  `/v1/barrier/root-token`, `/v1/barrier/officers/claim-status`,
  `/v1/barrier/unseal-share`, `/v1/ceremony/claim-cert/{begin,complete}`,
  `/v1/ceremony/officer-share/{begin,complete}`,
  `/v1/ceremony/claim-share/{begin,complete}`,
  `/v1/ceremony/enroll-custodian`, `/v1/ceremony/revoke-custodian`,
  `/v1/init/enroll-external-officer`,
  `/v1/tenants/{id}/barrier/{unseal-share,seal-vote}`,
  `/v1/tenants/{id}/barrier/officers/claim-status`.
- **gRPC/Unix RPCs**: `BarrierShamirUnsealShare(s)`, `BarrierRekey`,
  `BarrierOfficerClaimStatus`, `BarrierShamirSealVote`,
  `BarrierRecoveryRecover`, `BarrierRootToken`, `CeremonyClaimCert*`,
  `CeremonyBeginClaimShare`, `CeremonyCompleteClaimShare`,
  `CeremonyClaimShare*`, `CeremonyEnrollExternalOfficer`,
  `CeremonySubmitUnsealShare`, and `TenantBarrierService` `UnsealShare`,
  `OfficerClaimStatus`, `ShamirSealVote`. QUIC methods and MCP tools of the
  same operations are removed with them.
- **Permissions**: `seal:recover_with_keys`, `seal:submit_seal_vote`,
  `barrier:unseal-share`, `ceremony:enroll_custodian`,
  `ceremony:revoke_custodian`, `ceremony:claim_cert`,
  `ceremony:claim_share_begin`, `ceremony:claim_share_complete`,
  `tenant_barrier:claim_share`, `tenant_barrier:seal_vote`. The Custodian
  role now grants nothing; it is kept so persisted role references resolve.
- **CLI verbs**: `barrier unseal-shares`, `barrier shamir-seal-vote`,
  `barrier root-token`, `barrier recovery recover`, `barrier officer
  claim-status`, `barrier recovery officer …`, `ceremony claim-cert …`,
  `ceremony claim-share …`, `ceremony claim-share-begin|complete`,
  `ceremony submit-unseal-share`, `tenant barrier-shamir-seal-vote`, and
  `tenant barrier-unseal --share`.
- **`transport.TenantBarrierUnsealRequest.Share`**: the credential unseal
  takes `Key` only.
- **The User-PIN Shamir split in `qrdb init --officers`**: the ceremony now
  splits only the barrier's unseal key, to the named holders.
- **Credential-mode Shamir and legacy direct mode**: a Shamir barrier always
  holds its key under unseal-key indirection with a roster. A barrier whose
  wrapped blob predates the roster answers `seal.ErrHolderRosterMissing`;
  there is no migration (pre-release).
- **Dead error sentinels** of the removed flows in `pkg/ceremony`,
  `pkg/seal` and their `sdk/go` re-exports.
- **`tenant.SystemTenantID`, `tenant.IsSystemTenant` and `tenant.Normalize`
  moved to the new leaf package `pkg/tenantid`** as `tenantid.System`,
  `tenantid.IsSystem` and `tenantid.Normalize`. `pkg/tenant` imports
  `pkg/ceremony` and `pkg/auth`, so those packages could not name the system
  tenant; the leaf package imports nothing from this module. The SDK aliases
  (`sdk/go.SystemTenantID`, `IsSystemTenant`, `NormalizeTenantID`) are
  unchanged.
- **`ceremony.ShareService` is deleted.** It stored raw Shamir share bytes in
  the replicated store (`SubmitShare`) and returned them (`ListShares`,
  `GetShare`), contradicting the rule that share material is never persisted.
  Removed with it: `ceremony.ShareServicer`, `ceremony.NewShareService`,
  `ceremony.ShareServiceError`, `lifecycle.Node.Share`,
  `lifecycle.ShareServiceError`, the embedded `WithShareService` option,
  `transport.ShareClient` and its request/response types (`SubmitShareRequest`,
  `SubmitShareResponse`, `ShareInfo`, `ListSharesResponse`,
  `ShareCollectionStatus`), the MCP stubs, and the SDK's `Client.Shares()`,
  `sdk/go.ShareServicer`, `sdk/go.ShareService`, `sdk/go.NewShareService` and
  `sdk/go.CeremonyShareServiceError` aliases. There is no replacement: shares
  leave the node only through the barrier's ceremony distributor and the
  one-time claim.

### Changed — BREAKING

- **Holder enrolment and claim wire types.** `OfficerRegisterRequest` gains
  `role`, `encryption_kem`, `encryption_key`, `factors` and `designated`; its
  response carries the CA-issued certificate, role, designated factor, KEM
  and enrolled factors, and no share index. `ShareClaimCompleteResponse`
  carries the sealed `envelope`, not share bytes.
- **`BarrierInitializeShamirRequest`** takes `threshold`, `total_shares`
  (which must equal the enrolled holder count) and `policy`; there is no
  secret. Recovery generation takes `threshold`, `policy` and the SO PIN and
  splits to every enrolled recovery holder.
- **A desktop or embedded holder can claim its envelope.** The embedded
  transport has no connection to present a certificate, so its claims were
  refused outright. An in-process claim (`ceremony.WithInProcessClaim`) is now
  bound to the holder's enrolled certificate and released only for a
  signature over the fresh claim nonce made with that certificate's key, the
  proof the `mtls` quorum factor verifies. A presented certificate is still
  checked, and the network transports keep requiring one.
- **A share claim without a client certificate is Unauthenticated on every
  network transport.** gRPC and Unix answered `INVALID_ARGUMENT`, QUIC
  PermissionDenied and MCP Internal. The one typed error is now
  `service.ClientCertRequiredError`, classified Unauthenticated in
  `pkg/api/errclass` (REST 401, gRPC/Unix/QUIC/MCP `UNAUTHENTICATED`). The
  per-transport types it replaces are removed: `grpc.ClientCertRequiredError`,
  `quic.ClientCertRequiredError`, the REST and MCP unexported twins and the
  SDK's `embedded.ClientCertRequiredError`; match
  `*service.ClientCertRequiredError` instead.
- **The TypeScript SDK and web UI follow the share-holder model.** They called
  routes the server no longer serves (`/v1/barrier/shamir/unseal-share`,
  `/unseal-shares`, `/seal-vote`, `/v1/barrier/officers/claim-status`, the
  tenant `unseal-share` and `seal-vote`) and read share bytes from claims.
  `OfficerClient` now administers holders only: `registerOfficer`,
  `registerRecoveryOfficer`, `listOfficers`, `startRekey`, `revokeOfficer` and
  their tenant forms (`registerTenantOfficer`, `listTenantOfficers`,
  `startTenantRekey`, `revokeTenantOfficer`), with the enrolment fields `role`,
  `encryptionKem`, `encryptionKey`, `factors` and `designated`; the claim and
  seal-vote methods are removed. `SealClient.initializeShamir` takes
  `{threshold, totalShares, policy, soPin, userPin}` and no secret;
  `SealClient.unseal` and `TenantClient.barrierUnseal` serve passphrase
  barriers only, and `TenantClient.barrierUnsealShare` is removed. The web UI
  drops the officer claim page and the share and seal-vote inputs: a sealed
  Shamir barrier shows which command its holders run, and the Seal Manager
  enrols, lists, rekeys and revokes holders. Holder keys never enter the page;
  holders claim and submit from the CLI or the desktop app.
- **The desktop `SealBinding`**: `InitializeShamir(threshold, total, policy)`;
  `Unseal` refuses a Shamir barrier, which its holders unseal. The embedded
  node no longer auto-unseals a Shamir barrier.

## [0.33.2-alpha] - 2026-10-03

The audit chain verifier no longer reports a missing log on an honest trail while the log rotates. Highlights:

- **Fixed a false tamper result during rotation.** Rotation now carries a sequence counter, and verification retries while a rotation is in progress. A rotation that stalls, fails or panics can no longer hide a deleted log or a broken link.

### Fixed

- **Audit chain verification no longer reports a stalled rotation as a missing log.**
  `ChainFileLogger.VerifyChain`/`VerifyChainSet` judged a missing active file on its directory
  listings alone. A writer descheduled between a rotation's rename and its re-open -- on a loaded
  host, for longer than the verifier's whole retry budget -- left every attempt the same listing
  with the active name absent, and the verifier reported the audit log missing
  (`*FileError`, "file does not exist") on an honest trail. The logger now keeps a rotation
  sequence that `rotate()` makes odd across the rename and re-open -- even again on every return
  path, a failed rename or reopen and a recovered panic included -- and the verifier counts an
  attempt only if it read the same even value on both sides of its listings; a stalled rotation
  is a `*ConcurrentRotationError` ("the trail moved"), never a fault. It takes no lock, and the
  verifier no longer takes the writer's lock to read the path.

## [0.33.1-alpha] - 2026-10-03

A clustered SDK boot that fails can now be retried cleanly. Highlights:

- **A failed or cancelled clustered boot leaves the node sealed and retryable.** Every tenant barrier is sealed, and every data-plane accessor is cleared.
- **A retried boot is fully alive.** Lease reaping, the reclaim sweep and tenant shard reconciliation now run on a live context after a retry.
- **The readiness wait honours the caller's context.**

### Fixed

- **The readiness wait honours the caller's context.** `NewClusteredNode`,
  `Node.Unseal`, the per-tenant volume host and the dedicated raft shards
  waited on `context.Background()`, holding a caller that gave up for up to
  15 seconds; they now return the context's error promptly. A wait that has
  already found the shard serving reports success even if the context is
  cancelled at that moment. (The wait itself -- a founder returns only once
  a quorum has elected a leader and that leader has committed in its term,
  within a 15s budget -- shipped in 0.30.1-alpha; this entry is the ctx fix
  on top of it, not a new wait.)
- **A clustered SDK boot that fails can be retried.** A failed or cancelled
  boot left the barrier reporting unsealed with its key already wiped and no
  boot pending, so the next `Unseal` reported a node that never booted as
  ready. The node is now left sealed, and the next `Unseal` boots it.
- **A cancelled boot seals every tenant barrier, not only the system one.**
  On the tenant-isolation path, `BuildTenantInfra` unseals every auto-unseal
  tenant barrier (`BarrierRegistry.RestoreVolumeTenants`) before re-attaching
  each tenant's per-tenant Host; the failure defer sealed only the system
  `StorageBarrier`, so a cancellation landing in that reattach left every
  restored tenant's DEK live in memory while `IsSealed()` reported `true`.
  The defer now seals through the `BarrierRegistry` (`SealAll`) when one is
  present -- tenants, then the system barrier -- falling back to the bare
  barrier seal otherwise, and clears the host and feature gate a failed
  attempt built (restoring the pre-attempt services node rather than the
  stopped post-boot one) so a retried `Unseal` never hands out a component
  the failed attempt already closed. `wireClientAndServer` publishes the
  `AuthFactorService`, `UserAuthFactorService` and `ABACPolicyReloader`
  handles, and the scoped encryption migrator, before steps that can still
  fail -- an admin-binding check honouring `ctx`, or the multi-protocol
  server failing to start -- so the same failure left those accessors
  handing out services built over the stopped host too. The defer now also
  calls `retractDataPlaneServices` and clears the migrator, mirroring
  `Close`, unbinds the auth-chain vocabulary (`n.authMethods`) so it falls
  back to the built-in tokens instead of consulting a middleware built over
  the stopped `svcNode`, and detaches the replicated volume-pool cursor and
  the cluster-wide tenant-slot store (new `ReplicatedStateStore.Unbind` and
  `BarrierRegistry.DetachReplicatedSlotStore`) -- defensive hygiene against a
  dangling reference to the stopped replicated KV, not a fallback that
  serves a request: a failed boot leaves the node in its pre-boot SEALED
  state, and `TenantService.CreateTenant` fails closed (`seal.ErrSealed`)
  before reaching a pool slot either way, because its first read goes
  through the system barrier itself. A `SealAll` failure is now logged
  rather than discarded.
- **The retried boot's background goroutines now run on a live context.**
  The lease-expiry sweeper, the reclaim sweeper and (when tenant isolation
  wires one) the tenant shard reconciler all run on `Node.background()`'s
  node-lifetime context; the wire-failure branch cancelled `bgCancel` but
  left `bgCtx` set to the now-cancelled context, and `background()` makes a
  fresh one only when `bgCtx` is nil -- so a retried boot's sweepers and
  reconciler ran on a context already `Done` and exited at once: no lease
  reaping (`#174` regressing), no reclaim sweep, no reconcile. The failure
  defer now clears `bgCtx` alongside `bgCancel`, and a new `bgWG` tracks
  each of those goroutines so the wire-failure branch can wait for them to
  actually exit before `svcNode.Stop` -- cancelling alone does not block
  until a goroutine's current tick notices, so a bare cancel raced
  `svcNode.Stop` against whichever of them woke up last.

## [0.33.0-alpha] - 2026-10-03

Per-tenant compaction, database wizard encryption, and discovery parity. This release contains BREAKING changes; see the entries below. Highlights:

- **Compaction is per tenant database, never store-global.** Each database gets its own compaction floor. Reads above the floor keep etcd semantics: the value live at the floor stays readable. Another database's compaction never cancels your watches.
- **Scheduled compaction policies are replicated state.** They live in the database's own store and run on the group leader only, so they survive failover and restart and are covered by backup. The platform node-keyspace policy is set through a new platform-only verb (`admin:node_compaction`) on every transport, not node config.
- **One feature-gate error:** `featuregate.NotActiveError` (503 / Unavailable) replaces the per-feature errors.
- **Desktop database wizard:** collects an encryption passphrase and actually creates the barrier. Shamir is disabled until per-database share holders ship.
- **Raft discovery parity:** the Go SDK, the TS SDK and MCP can set every discovery block. Secrets stay write-only and are bound to their destination, including top-level peers.

### Fixed

- **The desktop database wizard's barrier works.** It sent the barrier block
  without `enabled` and with no secret, so the server never created a barrier
  and the step had no effect. Choosing the passphrase barrier now asks for the
  passphrase and a confirmation and creates the database with `enabled: true`
  and that passphrase. The passphrase is write-only on the client: it is
  cleared once the create request is built (whether the create succeeds or
  fails) and when the wizard is cancelled, never logged, and shown nowhere --
  the summary says only that it is set. The barrier now defaults to none, so
  a barrier is chosen, never implied. Leaving the wizard by any route clears
  the passphrase. The Shamir card is disabled, with a tooltip, until
  per-database Shamir share holders ship, and the create step refuses a
  Shamir barrier itself should the card ever let one through; a per-database Shamir barrier
  request -- from any client -- is refused with
  `database.UnsupportedBarrierStrategyError` as 400 / `INVALID_ARGUMENT`
  instead of 500. The TypeScript SDK's
  `CreateDatabaseRequest.barrier` is a `DatabaseBarrierRequest`, which carries
  the `secret`.

### Added

- **Per-database scheduled compaction, off by default.** A database's owning tenant may set
  a compaction policy -- `revision` (keep the store's last N revisions) or `period` (keep the
  history newer than a duration) -- and the compaction scheduler
  (`server.DefaultCompactionInterval`, one minute; `server.WithCompactionInterval`) compacts
  that database, and only that database. A database without a policy is never compacted
  implicitly. **The policy and a period policy's revision samples are replicated state in the
  database's own store** -- one reserved record (`"\x00qrdb.compaction/" + scope`,
  `pkg/compaction`) beside the durable lease table, written only through the new Raft opcode
  `OpCompactionState` (16) behind the `prefix-compaction` cluster feature -- never the node's
  catalog and never a file: every replica holds it, snapshots and backups carry it, the
  replica hash covers it, and only the leader of the database's group runs it, so a leader
  change continues the schedule, retention clock included, and a standalone node keeps it
  across restarts. Samples are capped at `retain_period`/64 per period; samples the store's
  revisions cannot have produced are never a target; every scheduled proposal carries the
  database's drop fence for the incarnation the policy was set for, and dropping a database
  clears its record. On every transport: REST
  `PATCH /v1/databases/{name}/compaction`, gRPC and the Unix socket `DatabaseUpdateCompaction`
  (the policy is `DatabaseInfoResponse.compaction`, protobuf field 17),
  QUIC `db.UpdateCompaction`, MCP `qrdb_database_update_compaction`, and the embedded
  transport; the Go SDK `DatabaseClient.UpdateDatabaseCompaction` and `DatabaseInfo.Compaction`;
  the CLI `qrdb database update-compaction --mode --retain-revisions --retain-period`; the
  TypeScript SDK `client.database.updateCompaction(name, compaction?)` with
  `DatabaseCompactionConfig`/`CompactionMode` and `DatabaseInfo.compaction` (REST and the
  desktop client's `DatabaseBinding.UpdateCompaction`). There is no configuration-file form:
  a `compaction` key under `databases[]` is not read. Setting a policy requires
  `mvcc:compact` (`ScopeSelfStrict`), never
  `databases:update`, and create does not take one; every caller, the system tenant included,
  reaches only its own databases (another tenant's answers as missing). An invalid policy is
  `compaction.InvalidPolicyError` (400 / `INVALID_ARGUMENT`). Proved on all six
  transports in `sdk/go/conformance` (`TestCompactionPolicy_OnEveryTransport`); across a
  leader failover of a three-node cluster and a standalone restart in `sdk/go/node`
  (`TestClusteredNodes_CompactionContinuesAfterLeaderFailover`,
  `TestStandaloneNode_CompactionScheduleSurvivesRestart`); and through snapshot recovery and
  the replica hash in `pkg/store` (`TestCompactionState_*`).
- **Platform node-keyspace compaction, off by default.** A policy of the same
  `off|revision|period` type as a database's compacts the node keyspace -- every key outside
  all `<tenant>/<db>/` namespaces: the node's `__sys:` records and the system tenant's raw
  keyspace -- on the compaction scheduler. It is set by a new platform-only verb on every
  transport: REST `GET`/`PUT /admin/node-compaction`, gRPC and the Unix socket
  `GetNodeCompaction`/`SetNodeCompaction`, QUIC `admin.GetNodeCompaction`/
  `admin.SetNodeCompaction`, MCP `qrdb_node_compaction`/`qrdb_update_node_compaction`, and the
  embedded transport; the Go SDK `AdminClient.NodeCompaction`/`SetNodeCompaction`; the CLI
  `qrdb admin node-compaction` and `qrdb admin update-node-compaction --mode
  --retain-revisions --retain-period`; the TypeScript SDK `client.admin.nodeCompaction()` /
  `setNodeCompaction(compaction?)` (REST and the desktop client's `AdminBinding`). Both need the
  new permission `admin:node_compaction`, which is `ScopeCross` -- only a system-tenant identity
  can exercise it; the built-in Admin role carries it -- and the admin service also refuses a
  caller bound to any other tenant (`tenant.CrossTenantDeniedError`), so a tenant's Admin, or a
  tenant role granted the permission, is refused on all six transports. The policy is the
  `compaction.NodeScope` record of the shared store's replicated compaction table, written by
  `OpCompactionState` like a database's and run by the shared store's leader only. The
  compaction skips every key with a database namespace by construction
  (`tenantid.NamespaceOf`), so no tenant database's history, the system tenant's own included,
  is touched; its floor (`[0x06]`) is kept apart from the per-database floors. New Raft opcode
  `OpMVCCCompactNode` (15) behind the cluster feature `node-compaction`; new
  `service.NodeKeyspaceCompactor` and `mvcc.Store.CompactNodeKeyspace`/`CompactNodeKeyspaceInBatch`;
  `service.NodeCompactionUnavailableError` (422 / `FAILED_PRECONDITION`) on a node whose shared
  store keeps no MVCC history. There is no configuration-file form: `mvcc.node_compaction` is not
  read. Proved on all six transports, a tenant refused on each, in `sdk/go/conformance`
  (`TestNodeKeyspaceCompaction_OnEveryTransport`).

### Security

- **MVCC compaction no longer destroys other tenants' history.** Every prefix-isolated
  tenant's databases share one MVCC store, and `Compact` discarded every key's history below
  the revision -- so the system tenant, the one identity `mvcc:compact` admitted, destroyed
  every tenant's time-travel history, as-of reads, watch replay window and restore window in
  one call. Compaction is now per database: the store keeps a compaction floor per keyspace
  prefix, a database's namespaced view compacts only its own `<tenant>/<database>/` prefix
  (its whole shard under raft isolation), and reads, watches, restores and change scans are
  checked against the floors their keys fall under. No one compacts another tenant's history:
  `mvcc:compact` is `ScopeSelfStrict`, so the system tenant compacts only its own databases,
  through the etcd listener as on every other transport. Proved on all six transports in
  `sdk/go/conformance` (`TestCompaction_*`).

### Changed — BREAKING

- **BREAKING: one refusal for every feature-gated operation.** `shard.FeatureNotActiveError`
  is replaced by `featuregate.NotActiveError` (`Feature`, and `ShardID` when the feature is
  needed of one group's replicas), which every operation that waits on cluster-feature
  activation returns: a raft database create before `cluster-shard-ids`, and a prefix, node
  or compaction-state command before `prefix-compaction` or `node-compaction`. It is classified
  Unavailable on every transport (REST 503, gRPC/Unix `UNAVAILABLE`, QUIC
  `StatusUnavailable`) because it clears once activation completes. `shard.WithActivation`
  takes a `featuregate.Feature`.

- **BREAKING: `Compact` compacts the scoped database's history, and only its owning tenant
  may call it.** It used to compact the whole store and was reserved to the system tenant;
  `mvcc:compact` moved from `ScopeCross` to `ScopeSelfStrict`, and `sdk.PermissionMVCCCompact`
  (a cross-scope export) is removed. A tenant role holding `mvcc:compact` -- the built-in
  Operator does -- can now compact its own databases. `service.CompactSystemOnlyError` is
  removed.
- **BREAKING: there is no store-global compaction.** The raw shard service's `Compact` refuses
  with `service.CompactScopeRequiredError` (REST 422, gRPC/QUIC `FAILED_PRECONDITION`): the
  unscoped embedded client, a server built without a service registry, and
  `mvcc.Store.Compact`/`CompactInBatch` and `mvcc.RaftStorage.Compact`/`CompactedRevision` are
  gone. Keys outside every database namespace are compacted only by the platform's
  node-keyspace policy (see Added), never by `Compact`.
- **BREAKING: Raft opcode 13 (`OpMVCCCompact`) is retired** and replaced by
  `OpMVCCCompactPrefix` (14, payload `[revision:8][prefixLen:2][prefix]`), proposed only once
  the new cluster feature `prefix-compaction` is enabled (`featuregate.NotActiveError`, 503 /
  `UNAVAILABLE`, before then). A log holding opcode 13 is refused as an unknown opcode. The compacted marker
  `[0x04]` becomes one floor key per prefix (`[0x04][prefix]`); an existing `[0x04]` loads as
  the floor of the empty prefix.
- **BREAKING: compaction validates its revision.** Below 1 is
  `service.CompactRevisionInvalidError`, above the current revision
  `service.CompactRevisionFutureError` (etcd: `ErrGRPCFutureRev`), at or below the database's
  floor `service.RevisionCompactedError` (etcd: `ErrGRPCCompacted`); the state machine
  re-checks all three at apply and answers in the result. A compacted window of
  `MVCCScanChangesQuery` is now refused instead of returning an empty key set, and the etcd
  listener's `Range` below the floor answers `ErrGRPCCompacted` instead of `Internal`.
- **BREAKING: Go API.** `statemachine.MVCCStore.Compact` (`sdk.MVCCStore`) is removed: no
  implementation existed, and the state machines of `pkg/statemachine` -- regular, concurrent,
  fast and kv -- no longer dispatch compaction, refusing it as an unknown opcode like any verb
  they are not built with; MVCC compaction is the MVCC engine's (`pkg/engine`).
  `watch.HistoryReplayer.CompactedRevision(key, rangeEnd []byte)`
  (was `CompactedRevision()`), `mvcc.Store.CompactedRevision` replaced by
  `CompactionFloor`/`CompactedRevisionForKey`/`CompactedRevisionForRange`/`CompactedRevisionForPrefix`,
  and `mvcc.CompactedKey` by `CompactionFloorKey(prefix)`. New: `service.PrefixCompactor`,
  `featuregate.PrefixCompaction`.
- **BREAKING: `mvcc.compaction_retention` and `mvcc.revision_limit` are removed.** Nothing read
  either key; both advertised a node-wide retention that would compact every tenant's history
  implicitly.

### Fixed

- **Concurrent service construction no longer panics on metric registration.**
  `metrics.NewCounter`, `NewGauge` and `NewHistogram` registered outside the lookup's lock, so
  two nodes building the same services at once in one process both registered a metric and
  the second panicked with a duplicate registration.
- **A change scan reads only its own keyspace.** A watch replay or an
  `MVCCScanChangesQuery` of one database read every key's data entries in the change log
  before filtering, so another database compacting mid-scan failed it -- cancelling the first
  database's watch with a compaction error naming its own floor. `mvcc.Store.ScanChanges` now
  takes the caller's key matcher and applies it before reading anything else.
- **A past-revision `Range` answers for every key live at that revision.** It enumerated the
  current keys, so a key deleted since was silently left out; it now enumerates the keys with
  a data entry in the range, and a `CountOnly` range at a revision counts the keys live then.
- **A read racing a compaction reports the compaction.** `GetAtRevision` and `Range` checked
  the floor and then read with no snapshot, so a compaction committing in between made the
  read come back absent. A compaction now publishes its floor before its deletions are
  visible, and every read at a revision re-checks the floor after reading
  (`mvcc.RevisionCompactedError`).
- **Compaction drops a tombstone live at the floor**, as etcd does, whether or not the key was
  written again; the apply path refuses a prefix compaction whose prefix is not exactly one
  database's keyspace.
- **Compaction keeps the value live at the floor.** Compacting to a revision kept only each
  key's newest entry, so a read above the floor but below a key's newest write -- a key written
  at 1, 3 and 5, compacted to 3, read at 4 -- answered "revision not found", and a `Range` at
  that revision silently left the key out, breaking the contract a client listing just above
  the compaction point (`kube-apiserver`) relies on. Compaction now follows etcd's rule: each
  key keeps its newest entry at or below the revision and everything above it, and loses only
  older entries; a kept tombstone goes when the key has a newer entry. Both per-database and
  node-keyspace compaction. Proved in `pkg/mvcc` (every floor of a fixture), through the apply
  path in `pkg/engine`, and on all six transports
  (`TestCompaction_KeepsTheValueLiveAtTheFloorOnEveryTransport`).
- **etcd listener: a `Watch` from a compacted revision is etcd's compacted watch.** A start
  revision at or below the database's compaction floor was refused with a response an etcd
  client cannot match to its create, so `clientv3`'s `Watch` call never returned. It is now
  created and then cancelled with `CompactRevision` set to the floor, which the client reports
  as `rpctypes.ErrCompacted`; a watch whose replay compaction overtakes ends the same way. The
  hub's refusal is the new `watch.CompactedError`, which matches `watch.ErrCompacted` and
  carries the floor covering the watched range. Any other refused create is etcd's failed
  create (`Created`, `Canceled`, watch ID `-1`, the reason) instead of a hang, a watch that
  ends for a reason is cancelled with it instead of going silent, and the stream's sends are
  serialized (gRPC forbids concurrent sends on a stream, and each watch is forwarded on its own
  goroutine). Proved against the etcd client itself (`TestEtcdClient_*` in `pkg/api/etcd`).

- **MCP reaches every Raft field, and a Raft update keeps the blocks it sends
  back.** `qrdb_database_create` and `qrdb_database_update_raft` built only the
  discovery block `discovery_method` named, so every MCP Raft update removed the
  other blocks and their secrets, and MCP had no arguments for the top-level
  `peers` or `localNodeCount`, the multicast `announceInterval` and
  `discoverTimeout`, the DNS `lookupTimeout` or the token `timeout`. Both tools
  now take all of them (`raft_peers` / `peers`, `raft_local_node_count` /
  `local_node_count`, `multicast_announce_interval`,
  `multicast_discover_timeout`, `dns_lookup_timeout`, `token_timeout`), and a
  block is sent when `discovery_method` names it or any of its arguments is
  given.
- **Both SDKs can set Raft discovery.** The SDKs' Raft request type had no
  discovery fields, so no SDK caller could set discovery on any transport, and
  the Go SDK's MCP transport also dropped `peers` and `localNodeCount`. The Go
  SDK's `transport.DatabaseRaftConfig` and the TypeScript SDK's
  `DatabaseRaftConfig` now carry `discovery` and the `static`, `multicast`,
  `dns` and `token` blocks (`StaticDiscoveryConfig`, `MulticastDiscoveryConfig`,
  `DNSDiscoveryConfig`, `TokenDiscoveryConfig`) with their write-only secrets,
  and every Go SDK transport -- REST, gRPC, Unix, QUIC, MCP and embedded --
  sends all of it. gRPC and Unix share one request converter. The TypeScript
  `DatabaseRaftInfo` no longer extends `DatabaseRaftConfig`, since a response
  block reports a secret only as set; it declares the same top-level fields.
  The TypeScript SDK now also exports `DatabaseRaftInfo` and the discovery
  info and request types.

### Security

- **A kept static discovery secret stays with the top-level peers too.**
  Static discovery reaches a database's top-level Raft `peers` as well as the
  static block's, but a Raft update compared only the static block's, so
  re-pointing the top-level `peers` without the secret kept the stored secret
  for the new peers. The static destination is now the union of both lists
  (when `discovery` is `static` or unset), compared as a set; changing it
  without the secret is refused with `database.DiscoverySecretRebindError`.
  Destinations compare literally -- no name resolution or normalisation --
  which `docs/databases/api.md` now states.

## [0.32.0-alpha] - 2026-10-02

Strict tenant isolation, phase 2: cluster-wide shard IDs. This release contains BREAKING changes; see the entries below. Highlights:

- **Raft shard IDs are unique across the cluster.** They are claimed from one replicated record, in standalone and Raft modes alike. Two nodes can no longer create two Raft groups under one ID, and a dropped database's ID is never issued again. The node-local `shard-high-water` file and the per-node allocator are removed.
- **SECURITY, BREAKING: callers can no longer choose a shard ID.** The field is gone from database create and Raft update on every transport, both SDKs and the CLI (gRPC field reserved). Raft creates are refused until the `cluster-shard-ids` feature is active. A cluster that already holds raft-isolated databases must be rebuilt rather than upgraded in place.
- **Raft discovery secrets on update:** an omitted secret is kept only while its destination (peers, multicast group, token endpoint) is unchanged. Re-pointing a secret without resending it is refused.
- **`storageCodec`** is reported on every transport. Database configuration validation errors are 400s, not 500s. `make test-typescript` mirrors the CI job.

### Security

- **BREAKING: a database create can no longer choose its shard ID, on any
  transport.** A raft database create claimed a cluster-wide shard ID only when
  the request named none. A request that named one -- `shardId` on REST, QUIC
  and the SDKs, `shard_id` on gRPC, `raft_shard_id` on MCP, `--shard-id` on the
  CLI -- was recorded in the receiving node's ledger alone and never checked
  against what other nodes hold. One tenant could therefore place a database on
  another tenant's Raft group, or on a reserved shard such as the system shard.
  What changed:
  - The field is removed from every transport's create and Raft-update request:
    - REST, QUIC and MCP JSON (the key is now ignored);
    - gRPC, which reserves field 2 of `DatabaseRaftConfig`;
    - the Go and TypeScript SDKs' `DatabaseRaftConfig`;
    - the CLI's `database update-raft --shard-id`.
  - A database's shard ID is reported only as `DatabaseInfo.shardId`.
  - As defence in depth, `database.Manager` refuses a caller's shard ID with
    `database.CallerShardIDError` (400 / `INVALID_ARGUMENT`), and
    `Manager.UpdateRaft` keeps the issued ID.

### Added

- **`make test-typescript`** runs exactly what CI's `test-typescript` job runs
  -- the TypeScript SDK's `npm ci`, `tsc`, `npm test` and build, then
  `svelte-check` on the desktop frontend against the built SDK -- and the job
  now runs the target.

### Fixed

- **Every transport reports a database's storage codec.** `storageCodec` is
  in `DatabaseInfo` on the wire (protobuf `storage_codec = 16`), in the Go SDK
  (`DatabaseInfo.StorageCodec`) and in the TypeScript SDK. QUIC and MCP had
  reported it only while they sent the internal record, and no transport did
  once every response came from one converter. **BREAKING:** the Raft block of
  a response no longer repeats the shard ID -- the database's is
  `DatabaseInfo.shardId`; protobuf reserves field 2 of `DatabaseRaftInfo`, and
  the Go SDK's `DatabaseRaftInfo.ShardID` is removed.
- **The user shard range belongs to databases.** Shard IDs from
  `shard.UserMin` (1000) up are issued by the cluster to databases. A node's
  `raft.shard_id` in that range is a configuration validation error, and an
  admin start-shard naming one is refused with `service.UserRangeShardError`
  (400 / `INVALID_ARGUMENT`) on every transport.
- **A Raft update keeps the secrets it leaves out.** Secrets are write-only,
  so a client that read a database's Raft settings and wrote them back sent
  none: the update cleared the static shared secret and failed validation for
  multicast and token discovery. A discovery block without its secret now
  keeps the stored one; sending a secret rotates it; leaving a block out
  removes it. A kept secret stays with what it authenticates: an update that
  changes the static peer set, the multicast group or interface, or the token
  endpoint without resending the secret is refused
  (`database.DiscoverySecretRebindError`, 400), so a caller cannot point a
  secret it never saw at a destination of its choosing. MCP now builds the
  static block whether or not the request has a secret, and takes its peers
  (`static_peers`), as the other transports do. A Raft update that would remove a raft-backed database's Raft
  configuration -- its shard ID with it -- is refused
  (`database.RaftConfigRequiredError`, 400). Database configuration
  validation failures are 400 / `INVALID_ARGUMENT` on every transport instead
  of 500. `database.WithShardIDClaimer`, added in this cycle, is removed: no composition has the
  replicated store when it builds the Manager, so `SetShardIDClaimer`, now
  safe to call while the Manager serves, is the one way to set the claimer.
- **Shard IDs are unique across the cluster, not only per node.** Every
  node's allocator started at 1000 and moved past only the IDs that node had
  issued, so two nodes creating raft databases were handed the same shard ID:
  two Raft groups under one ID. A database dropped on one node and created
  again through another could also be handed the dropped incarnation's ID,
  which the drop fence relies on never happening.
  - **How IDs are issued now.** Every raft database create claims its ID from
    one replicated high-water mark in the system store
    (`__sys:shard/high-water`, `shard.Claimer`) with a compare-and-swap, in
    standalone and raft mode alike.
  - **No node-local shard state.** The per-node counter (`shard.Allocator`,
    `database.WithAllocator`) and its `{dataDir}/shard-high-water` file are
    removed, so a node restored from a backup onto an empty data directory
    never re-issues an ID. Nothing migrates the old file.
    `Manager.SetShardIDClaimer` sets the claimer.
  - **Refusals.** `database.Manager.Provision` refuses a Raft configuration
    (`database.ProvisionRaftError`), because nodes provisioning on their own
    cannot agree on a shard ID; no shipped path provisions one. A create that
    cannot claim refuses with `database.ShardIDClaimError` (503 / retryable)
    instead of falling back to a per-node ID.
  - **New cluster feature `cluster-shard-ids`.** Once every node claims, a
    binary that allocates per node can neither start nor be admitted as a
    replica.
  - **BREAKING: raft creates wait for `cluster-shard-ids`.** On a node whose
    groups can hold other binaries, a raft database create is refused with
    `shard.FeatureNotActiveError` (503 / `UNAVAILABLE`, retryable) until the
    feature is active cluster-wide, and nothing is claimed. That closes the
    rolling-upgrade window in which an old node allocates per node while a new
    one claims.
  - **BREAKING: a cluster that holds raft databases cannot be upgraded in
    place.** Their shard IDs were issued per node and may collide, and the
    high-water mark could re-issue one. Rebuild such a cluster on this release
    and move the data across; nothing seeds or migrates the old IDs.
  - This is phase 2 of the strict tenant isolation plan.

## [0.31.0-alpha] - 2026-10-02

Dependency release. This release contains BREAKING changes; see the entries below. Highlights:

- **go-quicraft v0.16.0, go-truststrap v0.4.0-alpha, go-enclave v0.6.2-alpha.** go-enclave v0.7.0-alpha is held back: go-quicraft v0.16.0 does not compile against it. That needs a go-quicraft release built on enclave v0.7.
- **BREAKING (wire):** go-truststrap v0.4 adds a suite header to the Noise bootstrap wire. qrdb runs the bootstrap server itself, so a mixed-version cluster cannot enroll a new member. Finish a rolling upgrade before adding nodes.
- **Third-party patch and minor updates** (pebble 2.1.7, etcd 3.7.2, pgx 5.11, chi, mysql driver, roaring, mongo-driver, x/net, x/tools). Held back: grpc stays on v1.83.2 because v1.84.0 introduces GO-2026-6443; psql-wire and mcp-go stay at their current majors.
- **`make vulncheck`** runs govulncheck in the root and sdk/go modules.
- **Fix:** the file storage engine's `List` returns raw keys instead of their percent-encoded paths.

### Dependencies

- **go-quicraft v0.15.0-alpha -> v0.16.0** (the project's first non-`-alpha`
  tag) in the root, `sdk/go` and `sdk/go/gorm/conformance` modules, in
  lockstep as the dependency-resolution convention requires.
- **go-truststrap v0.2.0-alpha -> v0.4.0-alpha.** **BREAKING (wire,
  upstream):** every bootstrap connection's first frame now carries a
  two-byte `version || suite` header, incompatible with the
  v0.2.0-alpha/v0.3.0-alpha wire. **This is qrdb-to-qrdb, not qrdb-to-an-
  external-dependency:** `pkg/clusterenroll` runs both halves of the
  handshake with this release's go-truststrap. The first node of a cluster
  runs the Noise bootstrap **server** (`startNoiseAdvertiser` /
  `noiseboot.NewServer`, `noiseboot` being
  `go-truststrap/pkg/noiseproto/bootstrap` directly) from the moment it
  generates or loads the cluster CA, on every boot (`ResumeAdvertiser`); a
  joining node is the **client** (`truststrap.AutoFetch`) once member-side
  CSR join ships (the join path is currently refused up front with
  `EnrollmentUnavailableError`, go-qrdb#241, so no client dials today, but
  the server is live now). A node on this release cannot complete the
  handshake against a peer's server on the old wire, and vice versa: **a
  mixed-version cluster cannot enroll a new member until every node able to
  advertise is on this release.** The fleet moves together; the old wire is
  obsolete, not a compatibility target. See
  [docs/clustering/user.md](docs/clustering/user.md#upgrading-a-cluster) for
  the operational note. `bootstrap.Server` was also made single-use in
  v0.3.0-alpha (restart removed); `pkg/clusterenroll` already builds one
  `*noiseboot.Server` per `Advertiser` and never restarts it in place, so
  this needed no qrdb change. Noted for `lane/ca-keys-replicated`, which is
  building member-side CSR join on `noiseproto`: the suite-negotiation API
  (`ServerConfig.P256StaticKey`, `ClientConfig.ServerP256StaticKey`,
  `ClientConfig.Suite`, `bootstrap.SuiteNoiseNK`/`SuiteP256AESGCM`) is new
  surface to adopt on that branch, not a removal.
- **go-enclave v0.6.1-alpha -> v0.6.2-alpha only** (bugfixes: a negative
  `BudgetError.InUse` race, several release/rollback accounting fixes, a
  `ModeRootKey` sealer no longer re-unsealing its root after a successful
  first unseal, sticky canary-corruption reporting across a refused
  release). **go-enclave v0.7.0-alpha, which the owner asked for by name, is
  explicitly HELD BACK.** v0.7.0-alpha is a breaking MINOR release threading
  `context.Context` through `NewSealed`, `(*Sealer).Seal`,
  `(*SealedEnclave).Open`/`OpenWith` and the `KeyProtector`/`RootUnsealer`
  provider seam, and freezing the plaintext `Open`/`OpenWith` return
  read-only. Go's MVS selects the highest version any module in the graph
  requires, so requiring go-enclave v0.7.0-alpha here while go-quicraft
  v0.16.0's own `go.mod` asks for v0.6.2-alpha would still select v0.7.0-alpha
  for go-quicraft's build too — and go-quicraft v0.16.0 does not compile
  against it: 15 call sites across `pkg/seal/{barrier.go,strategy.go,
  strategy_software.go}` call `enclave.NewSealed` and
  `(*SealedEnclave).Open` without the new `ctx` parameter. Reproduced in a
  scratch module (`GOWORK=off go build -gcflags="all=-e" ./...` against
  `github.com/jeremyhahn/go-quicraft/pkg/seal`); the same module builds
  go-quicraft v0.16.0 cleanly against v0.6.2-alpha, the version its `go.mod`
  already declares. go-quicraft needs its own go-enclave v0.7 adoption before
  qrdb can take it; see the upgrade guide at go-enclave `docs/README.md`
  ("Upgrading from v0.6 to v0.7") for the call shapes that migration needs.
- **go-objstore v0.3.0-alpha and go-codec v1.4.0 verified already latest** —
  no change.
- **Third-party, direct requires, patch/minor only:**
  `github.com/RoaringBitmap/roaring/v2` v2.15.0 -> v2.29.0,
  `github.com/cockroachdb/pebble/v2` v2.1.4 -> v2.1.7,
  `github.com/go-chi/chi/v5` v5.3.0 -> v5.3.2,
  `github.com/go-sql-driver/mysql` v1.9.3 -> v1.10.1 (root and
  `sdk/go/gorm/conformance`),
  `github.com/jackc/pgx/v5` v5.10.0 -> v5.11.0,
  `github.com/tidwall/btree` v1.8.1 -> v1.8.2,
  `go.etcd.io/etcd/api/v3` and `go.etcd.io/etcd/client/v3` v3.6.8 -> v3.7.2,
  `go.mongodb.org/mongo-driver` v1.17.9 -> v1.17.10,
  `golang.org/x/net` v0.58.0 -> v0.59.0,
  `golang.org/x/tools` v0.49.0 -> v0.51.0,
  `gorm.io/driver/postgres` v1.6.2 -> v1.6.3 (`sdk/go/gorm/conformance`
  only). `protoc`/`protoc-gen-go`/`protoc-gen-go-grpc` pins and
  `google.golang.org/protobuf` are already current and untouched.
- **Held back, with reasons:**
  - **`google.golang.org/grpc` stays at v1.83.2.** v1.84.0 is affected by
    **GO-2026-6443**, a server panic on a request missing the `:authority`
    pseudo-header, reachable through this repository's gRPC API server
    (`pkg/api/unix/server.go:725` and the main gRPC listener). No tagged
    grpc-go release fixes it yet and `govulncheck` against `dev/main`
    reports zero findings; taking v1.84.0 now would introduce a
    remotely-triggerable DoS. Re-evaluate once a patched tag exists.
  - `github.com/jeroenrinzema/psql-wire` v0.18.0 (latest v0.20.0 changes
    `ParseFn`'s signature; `pkg/sql/pgsql/server.go`'s `wire.NewServer` call
    no longer type-checks against it — a pre-1.0 breaking change this release
    does not adopt).
  - `github.com/mark3labs/mcp-go` v0.45.0 (latest v1.1.1 **builds** cleanly
    against every qrdb call site, but changes the MCP transport's
    unauthenticated-request error shape: `TestWithAuthenticators_BearerAuthenticatesOnEveryNetworkTransport/mcp`
    in `sdk/go/node` — the Tier-1 auth-parity test across all six transports —
    fails under it with "no credential: refusal is not unauthenticated:
    transport error: authorization required". This is a behavioral
    regression in a Tier-1 capability, not a compile break, so it is held
    back rather than silently shipped; confirmed green again at v0.45.0.
    Holding mcp-go back also means its transitive pulls
    (`buger/jsonparser`, `mailru/easyjson`, `google/jsonschema-go`) stay off
    every module's dependency graph; `jsonparser`/`easyjson` in particular
    must stay at v1.1.2/v0.9.2 — a stray trial of mcp-go v1.1.1 had briefly
    downgraded them to v1.1.1/v0.7.7 via MVS, and v1.1.1 reintroduces
    **GO-2026-4514**. `govulncheck` confirms zero findings at the restored
    versions.)
  - Every `cloud.google.com/go/*`, `github.com/aws/aws-sdk-go-v2/*`,
    `github.com/wailsapp/wails/v2`, and similar large transitive graphs
    pulled in only by `github.com/dolthub/go-mysql-server`'s indirect
    dependencies are untouched: they are not direct requires of this
    repository and move with `go-mysql-server`'s own releases.
- **TypeScript SDK (`sdk/typescript`) and desktop frontend
  (`cmd/qrdb/frontend`):** `npm outdated` and `npm update` run in both.
  `sdk/typescript`'s `typescript` and `vitest` are already at the latest
  version their declared ranges allow; the next versions available
  (`typescript` 7.x, `vitest` 5.x) are major bumps, held back. `tsc` and
  `vitest run` stay green (8 files, 183 tests). The frontend's
  `@fontsource-variable/inter`, `@playwright/test`, `@tailwindcss/vite`,
  `@types/node`, `daisyui`, `svelte` and `svelte-check` move to the latest
  version their ranges allow; `@sveltejs/vite-plugin-svelte`, `lucide-svelte`,
  `typescript` and `vite` have only major-version updates available and are
  held back. `svelte-check` is green (0 errors) after rebuilding
  `sdk/typescript`'s `dist/`, which the frontend's `file:` dependency on
  `@qrdb/sdk` resolves against.
- **`make vulncheck`**, backed by `govulncheck` (pinned alongside the
  repository's other analysis tools), runs in `make lint` (so the CI Lint
  job gates it) and is also runnable standalone per module
  (`make vulncheck`, `make vulncheck-sdk`). It is how GO-2026-6443 and
  GO-2026-4514 were caught before either reached `dev/main`.

### Fixed

- **`pkg/storage/file`'s `List` now decodes the percent-encoded path back to
  the raw key it returns**, found while extending a sibling fix's parity
  tests to the file backend. It was the one place in that engine that read
  the on-disk path without reversing `encodeKeyToPath`
  (`pkg/storage/file/keyenc.go`), so a key that needed escaping (anything
  outside printable ASCII, including a NUL-byte-prefixed key like the lease
  table's) came back from `List` as the literal percent-encoded string
  rather than the key that was `Set`, and a prefix search for the real
  (unescaped) prefix matched nothing even though the key existed.
  `Get`/`Set`/`Delete`/`Exists` and the iterator were unaffected; only
  `List` had the gap.

## [0.30.1-alpha] - 2026-10-02

Fix release. Highlights:

- **A shard is handed out only once it can serve a linearizable read.** Every host-open path now waits for the new leader to commit in its term, not just for a leader to be known: the tenant volume host, dedicated per-database shards, the SDK clustered node, the qrdbd system shard (before its listeners start), and the desktop embedded server. This fixes intermittent "read index not ready" failures right after a node or shard starts. `NewClusteredNode` now blocks until a quorum has elected and committed (within a 15s budget).

### Fixed

- **A shard is handed out only once it can serve a linearizable read.** Every
  path that opens or reopens a shard -- the per-tenant volume host, the
  dedicated per-database raft shards (including an encryption migration's
  reopen), the system shard before `qrdbd` serves, and the desktop embedded
  server -- waited only until a leader was known, and `sdk/go/node`'s
  `NewClusteredNode` waited for nothing unless tenant isolation was on: three
  founders booted together came back before any election. A leader elected a moment ago refuses every ReadIndex until it
  commits in its term, so the first read after an open could fail with
  `quicraft: read index not ready` (seen as intermittent failures after a
  tenant encryption migration and right after cluster formation). They now
  wait through `server.WaitForShardServing` for both, in one 15s budget, and
  time out with `*server.ShardLeaderWaitError` or the new
  `*server.ShardReadWaitError`. `qrdbd`'s best-effort ReadIndex warm-up is
  replaced by the wait, and a node that cannot serve refuses to start with
  `SystemShardServingError`. The desktop bindings' `LeaderElectionTimeoutError` is
  removed; a shard that cannot serve reports `ShardStartError` wrapping the
  wait's error.

## [0.30.0-alpha] - 2026-10-02

Database response hardening. This release contains BREAKING changes; see the entries below. Highlights:

- **SECURITY: a database's secrets are write-only on every transport.** Raft discovery shared secrets and tokens, and the barrier secret, were returned by get, list, create and update on REST, gRPC, unix, QUIC and MCP. Responses now carry `sharedSecretSet` / `tokenSet` flags and no secret field. A single converter (`pkg/api/dbwire`) builds every database response, and a single SDK decoder replaces the per-transport copies.
- **Parity:** QUIC and MCP report `shardId`, `dataDir` and `tenantId` again. `dedicatedWal`, the discovery blocks, `protocol.etcd` and `tenantId` are typed in both SDKs. The TypeScript SDK reads `shardId` and `dataDir`, the keys REST sends.
- **CI runs the TypeScript SDK and the desktop frontend's svelte-check.**

### Security

- **BREAKING: a database's discovery secrets are write-only.** Every read of a
  database -- get, list, and the response to every update -- returned the
  stored Raft discovery secrets (`raft.static.sharedSecret`,
  `raft.multicast.sharedSecret`, `raft.token.token`) on REST, gRPC, the Unix
  socket, QUIC, MCP, the embedded transport and the desktop bindings, to
  anyone allowed to read the database's configuration. No response carries
  them now; each is reported only as `sharedSecretSet` / `tokenSet`.
  - **Typed so it cannot come back.** The responses are read views with no
    field for a secret: in the Go SDK `DatabaseInfo.Raft` is a
    `transport.DatabaseRaftInfo` and `DatabaseInfo.Barrier` a
    `transport.DatabaseBarrierInfo` (callers of the old field types must
    move); in protobuf `DatabaseInfoResponse.raft` and `.barrier` are the new
    `DatabaseRaftInfo` and `DatabaseBarrierInfo`, with the secret field numbers
    `reserved` and the other numbers unchanged; in the TypeScript SDK
    `DatabaseInfo.raft` is a `DatabaseRaftInfo`. Request types still carry the
    secrets.
  - **One converter.** Every transport builds its response in `pkg/api/dbwire`
    (the gRPC and Unix SDKs decode it in `transport/internal/dbconv`); the
    per-transport copies are gone. QUIC and MCP had sent the server's internal
    snake_case record, which the Go SDK decoded as a zero shard ID, data
    directory and tenant; they now send the same shape as REST.
  - Proven on every transport by a test that stores each secret through the
    real service and reads it back.

### Added

- **Parity of `DatabaseInfo` across transports and SDKs.** `dedicatedWal` is
  now in the protobuf response (`dedicated_wal = 15`) and the Go SDK; the Raft
  discovery settings (method, static, multicast, DNS, token -- minus secrets)
  and `protocol.etcd` are typed in both SDKs; REST reports `tenantId` like gRPC.
  The Go and TypeScript wire contract tests hold both SDKs to the REST response
  with no exception list.
- **CI runs the TypeScript SDK and the desktop frontend.** A `test-typescript`
  job, required by the CI gate, runs `npm ci`, `tsc`, `npm test` and the build
  in `sdk/typescript`, then `svelte-check` in `cmd/qrdb/frontend` against the
  built SDK. The frontend's seventeen type errors are fixed; none changed what
  the frontend sends.

### Fixed

- **The TypeScript SDK reads a database's shard ID and data directory.**
  `DatabaseInfo` declared `shard_id` and `data_dir`, but REST (and the desktop
  bindings) send `shardId` and `dataDir`, so both were always undefined; the
  desktop database views never showed either. The fields are now `shardId` and
  `dataDir` (**BREAKING** for TypeScript callers that read the old names).
  A recorded REST response is now decoded by a Go handler test, which also
  holds the Go SDK's `transport.DatabaseInfo` to it, and by a TypeScript SDK
  test, and `npm test` in `sdk/typescript` typechecks the tests before running
  them. The desktop database wizard no longer carries a shard ID.

## [0.29.1-alpha] - 2026-10-02

Fix release. Highlights:

- **CI build gate green again.** v0.29.0's refused-unsigned-holder-forward path wrote a fixed 502 for a received error and failed the `test/buildgates` error-routing gate. It is now a `LeaderForwardError` like the other forwarding failures; behaviour is unchanged.

### Fixed

- **A refused unsigned holder forward is classified like every other forwarding
  failure.** v0.29.0 wrote the holder refusal straight as a 502, bypassing the
  error-mapping rule that `test/buildgates` enforces for REST handlers, which
  left that gate red. The refusal is now a `*rest.LeaderForwardError` wrapping
  `*rest.HolderForwardUnsignedError`, the same shape as the other two forwarding
  failures in `forwardToLeader`. The status (502), the fixed message and
  `errors.As` reachability of the holder error are unchanged.

## [0.29.0-alpha] - 2026-10-02

Holder-certificate hardening, following the v0.28.1 security hotfix. This release contains BREAKING changes; see the entries below. Highlights:

- **The holder mark survives leader forwarding.** A forwarded officer or custodian request is still recognised as a holder by the leader, and gets no roles. A holder request is never forwarded unsigned (502).
- **A holder certificate that can sign certificates is refused.** A CA certificate, cert-sign key usage, or any key usage or EKU set other than the one requested is refused on every holder entry point, whatever CA is composed in.
- **The CeremonyCA signing contract is stated as a MUST:** sign with `SignCSROptions.RawSubject()`, take nothing from the CSR but its key, and issue a leaf with exactly the requested usages.
- **`custodian` and `security-officer` are reserved tenant ids.** Holder profile names move to the new leaf package `pkg/holderprofile`.

### Security

- **A holder certificate stays a holder when its request is forwarded to the
  leader.** The on-behalf assertion a follower signs carried only the subject
  and tenant, so the leader resolved a forwarded holder certificate named
  `admin` to the `admin` account and its roles. The assertion now carries the
  holder profile (`forward.CallerAssertion.HolderProfile`, omitted for users
  so their assertions are unchanged), and the leader re-marks the identity; a
  profile it does not recognise is refused. A holder request is never
  forwarded unsigned, because an unsigned forward is authorized as the
  forwarding node: a follower without a signing identity, or whose signing
  fails, refuses it with 502 (`rest.HolderForwardUnsignedError`), whose body
  is a fixed message; the signing failure is logged and kept for `errors.Is`,
  never sent to the client. In a cluster mixing this release with an earlier
  one, an earlier leader ignores the field.
- **BREAKING: the holder profile names live in the new leaf package
  `pkg/holderprofile`.** `auth.ProfileCustodian` and
  `auth.ProfileSecurityOfficer` are removed in favour of
  `holderprofile.Custodian` and `holderprofile.SecurityOfficer`, with
  `holderprofile.Is` as the predicate; `auth.HolderProfile` uses it. There are
  no aliases. The leaf lets `pkg/auth` and `pkg/tenant` share the names without
  `pkg/tenant` depending on `pkg/auth` for them.
- **The ceremony refuses a holder certificate that is a sub-CA.** A
  `CeremonyCA` returning a certificate with the right subject was trusted with
  what the certificate could do. One that is a CA, carries certificate-signing
  key usage or any key usage other than the requested one (CRL signing
  included), or carries a set of extended key usages other than exactly the
  requested set (order aside, duplicates refused) is now refused with
  `*ceremony.IssuedCertCapabilityError` naming the field, on every signing
  path, and nothing is recorded. A holder certificate able to sign
  certificates would be a sub-CA under the cluster CA and could mint any login.
- **BREAKING: `custodian` and `security-officer` are reserved tenant ids.** An
  officer certificate issued before v0.28.1-alpha carries its profile as its
  first Subject OU, which the mTLS authenticator reads as the tenant, so a
  tenant by either name would be the tenant those certificates authenticate
  in. Creating or deleting one is refused with `*tenant.ReservedTenantIDError`
  (400 / `INVALID_ARGUMENT`), like `system` and `default`.

### Changed

- **The `CeremonyCA` signing contract is a stated MUST.** The `CeremonyCA` and
  `SignCSROptions` doc comments and docs/ceremony now say that every CA,
  including one composed in from outside qrdb, MUST sign with
  `SignCSROptions.RawSubject()`, carry no SAN, take only the CSR's key, and
  issue a leaf with exactly the requested key usage and extended key usages --
  and that a non-conforming CA's certificates are refused, fail-closed, with
  `*ceremony.CertSubjectError` or `*ceremony.IssuedCertCapabilityError`.

### Fixed

- **docs/shamir-custody/user.md no longer says an officer certificate reaches
  the unseal step.** It reaches the officer claim steps only; unseal shares are
  submitted as a user or an administrator. Documentation only.

## [0.28.1-alpha] - 2026-10-02

Security hotfix. This release contains BREAKING changes; see the entries below. Highlights:

- **CRITICAL: registering an officer can no longer mint a login.** Officer, recovery, `ceremony sign-csr` and initial-ceremony certificates name the registered holder and the scope's tenant; the CSR contributes only its key. Holder certificates authenticate with no roles.
- **Audit queries are confined to the caller's own tenant on every transport**, the system tenant included. `audit:read:cross` is removed.
- **No caller mints, lists or revokes another tenant's certificates**, the system tenant included. One revocation target is shared by every transport.

### Security

- **CRITICAL, BREAKING: registering an officer can no longer mint a login.**
  The ceremony CA is the node's cluster CA, which the mTLS listener trusts for
  client logins, and `LocalCA.SignCSR` copied the CSR's Subject and SANs. A
  tenant Admin registering a tenant-barrier officer with a CSR for
  `CN=admin, OU=system` received a certificate that authenticated as the
  system tenant's `admin` -- under the default `auth.mtls_binding_policy`, an
  account with no enrolled binding is admitted on CN and OU alone -- and
  `OU=globex` or a SPIFFE SAN for globex worked the same way. The same copy
  ran on system and recovery officer registration, `ceremony sign-csr` and the
  initial M-of-N ceremony. Every one of those paths now names the registered
  holder and the scope's tenant in `ceremony.SignCSROptions` (new `TenantID`
  and `Username`), and the CA issues `CN=<username>`,
  `OU=[<scope tenant>, <profile>]` -- one RDN per OU, so a long tenant id stays
  first -- with **no Subject Alternative Name**: the CSR contributes only its
  key. The ceremony re-checks every certificate a `CeremonyCA` returns
  (Subject, no SANs, the CSR's own key) and refuses one that differs with
  `*ceremony.CertSubjectError`, so a CA composed in from outside qrdb is held to
  the contract too (`SignCSROptions.Subject` and `RawSubject` state it). As
  defence in depth, a certificate carrying a holder profile OU
  (`security-officer`, `custodian`) is marked by the mTLS authenticator
  (`auth.HolderProfile`) and is never resolved to a user account: it
  authenticates with no roles and reaches only the identity-gated ceremony
  surface. The officer certificate layout changes from `OU=[<csr ou>...,
  <profile>]` to `OU=[<scope tenant>, <profile>]`; `ceremony.CSRTenantSANError`
  is removed. New leaf package `pkg/tenantid` holds the system tenant id that
  `pkg/ceremony` needs (`tenant.SystemTenantID` is it).
- **BREAKING: an audit query reads only the caller's own tenant's events, on
  every transport.** The query took `tenant_id` from the request unchecked on
  REST (`GET /v1/audit/events`), gRPC and the unix socket
  (`AuditService/Query`), QUIC (`audit.Query`), MCP (`qrdb_audit_query`) and
  the embedded client, and an omitted `tenant_id` read every tenant's events.
  The audit sinks are node-local and shared by every tenant, so a tenant Admin
  holding `audit:query` read the whole node's trail. Every transport now
  confines the filter with `tenant.ConfineAuditFilter` before a logger is read:
  an omitted `tenant_id` reads the caller's own tenant, and one naming any other
  tenant is refused (REST 403, gRPC and unix `PERMISSION_DENIED`, QUIC
  `StatusPermissionDenied`, the MCP permission-denied tool code, and
  `*tenant.CrossTenantDeniedError` on embedded). There is no system-tenant
  exception: a system-tenant caller reads the system tenant's events plus the
  platform events that carry no tenant (`audit.Filter.Untenanted`, never decoded
  from a request), and is refused every other tenant's. The tenant match runs
  before pagination, so no time range, filter or cursor widens it. New:
  `tenant.CallerTenant`, `tenant.Confine` and `tenant.ConfineAuditFilter`.
- **BREAKING: no caller can mint, list or revoke another tenant's
  certificates -- the system tenant included.** `certs:generate` was
  authorized against the request body's tenant under `ScopeSelf`, which the
  system-tenant rule lifts, so a system-tenant Admin could mint a CA-signed
  client certificate naming another tenant's user; under the default
  `auth.mtls_binding_policy` that certificate authenticates as the user.
  `certs:list` and `certs:revoke` had the same lift. `certs:generate`,
  `certs:list`, `certs:revoke`, `certs:read` and `certs:export` are now
  `ScopeSelfStrict`, so on REST, gRPC, unix, QUIC and MCP the target tenant
  must be the caller's own for every caller; a request naming another tenant
  is refused with the transport's permission-denied form. The embedded
  transport, which runs no authorization middleware, applies the same rule
  with `tenant.Confine`: `GenerateCert` and `ListCerts` confine the named
  tenant, and `RevokeCert` confines the serial's owner, or the system tenant
  for a deny-by-serial revocation (`*tenant.CrossTenantDeniedError`).
  `certs:revoke:cross` -- the deny-by-serial revocation of a serial no tenant
  owns -- stays `ScopeCross`. A platform tool that issued certificates into
  customer tenants must now run as each tenant's own Admin.
- **BREAKING: `audit:read:cross` is removed.** The permission was declared
  `ScopeCross`, granted to the built-in Admin role and re-exported as
  `sdk.PermissionAuditReadCross`, and nothing in qrdb enforced it -- yet its
  name promised a read of every tenant's audit trail. Under the owner ruling no
  caller reads another tenant's audit events, so the permission, its grant, its
  "Cross-Tenant Audit" category and its SDK name are deleted. A downstream
  consumer naming `sdk.PermissionAuditReadCross` no longer compiles. A custom
  role stored with `audit:read:cross` keeps the inert string, but creating or
  updating a role that names it is now refused with
  `*role.InvalidPermissionError`; the built-in Admin loses it on its next
  reconcile.

### Fixed

- **A custodian's audit events name its tenant.** Registration and revocation
  (refusals included) in `pkg/custodian`, and the share-claim and unseal-share
  steps in `pkg/ceremony`, now set `TenantID` to the custodian's tenant. With
  audit queries confined to the caller's tenant, an event without one is a
  platform event only the system tenant reads, so a tenant's auditors could
  not see their own custodians' activity.
- **One revocation target for every transport.** `middleware.CertRevokeTarget`
  states what a revocation is authorized as -- the serial's owner with
  `certs:revoke`, or the system tenant with `certs:revoke:cross` for a serial no
  tenant owns -- and both `AuthorizeCertRevoke` and the embedded transport use
  it.

- **The REST, QUIC and MCP SDK clients speak the audit query's wire shape.**
  The server's wire types are `audit.Filter` and `audit.Page`, whose JSON names
  (`tenant_id`, `client_ip`, `request_id`, `prev_hash`) differ from the SDK's
  `transport.AuditFilter` and `transport.AuditEvent`. The QUIC client sent the
  filter's tenant as `tenantID`, which the server never read, so a QUIC query
  silently ignored its tenant; and the REST, QUIC and MCP clients decoded every
  event without its tenant, client IP, request id and chain hash. They now
  convert through `sdk/go/transport/internal/auditconv`, which the embedded
  client shares.

## [0.28.0-alpha] - 2026-10-02

SQL correctness. This release contains BREAKING changes; see the entries below. Highlights:

- **Autocommit statements are atomic.** Each one runs as an implicit one-statement transaction: all its rows commit together, or none do. Its row-lock release is folded into the same write. The statement is bounded like a transaction (8 MiB / 100,000 operations).
- **`SET autocommit = 0` is a real multi-statement transaction:** COMMIT keeps, ROLLBACK discards.
- A statement that writes both relational and kv/entity/collection tables is refused.
- **Every SQL surface resolves each table once per statement:** MySQL wire, PostgreSQL wire, query service and embedded driver.
- **Embedded driver:** within a `sql.Tx` or `sql.Conn`, running a statement while a query's rows are open is refused, as the network MySQL/PostgreSQL drivers do. Ping stays valid. A reset with any statement still open discards the connection instead of handing on its transaction and locks.
- **Row locks:** one lease per connection, with a revoke fallback that deletes records. A fresh owner on a dead lease retries once.

### Changed

- **Behaviour: an autocommit SQL statement is atomic.** A statement outside
  an explicit transaction now commits all of its rows or none, as in InnoDB
  and PostgreSQL. A three-row `INSERT` whose third row is a duplicate leaves
  the first two absent; previously each row was its own commit and they
  stayed. This holds on the MySQL and PostgreSQL wires, the ad-hoc query
  service (REST, gRPC, Unix, QUIC, MCP, embedded) and the embedded driver.
  Reads, commit preconditions and duplicate reporting (1062, including a key
  taken concurrently before the commit) are unchanged; `qrdb_isolation` does
  not apply to an autocommit statement. A statement that writes two
  databases is refused. The statement's row-lock release is folded into its
  commit, so a pessimistic single-row `UPDATE`/`DELETE` is two Raft
  proposals instead of three. The query service and the PostgreSQL wire now
  report an error raised when a result is closed -- the commit -- instead of
  dropping it.
- **BREAKING (behaviour): an autocommit SQL statement is bounded like a
  transaction -- 8 MiB and 100,000 buffered operations.** A bulk
  `INSERT`, `UPDATE` or `DELETE` outside a transaction previously
  committed row by row and had no statement-wide limit; one that exceeds
  either bound now fails with `provider.WriteSetLimitError` and writes
  nothing. Split such a statement into batches below the bounds.
- **BREAKING (behaviour): a SQL statement writing a relational table and a
  `kv`, entity or collection table is refused**
  (`provider.MixedTransactionalWriteError`) before it writes either. The
  relational half is now atomic and the other applies at once, so such a
  statement could fail with its non-transactional half already committed.
- **BREAKING (behaviour): within a `sql.Tx` or `sql.Conn` on the embedded
  driver, running a statement while a query's rows are open is refused**
  (`provider.CommandInProgressError`), as the network MySQL and PostgreSQL
  drivers refuse it; read or close the rows first. In 0.27.0-alpha such a
  statement ran; each statement is now a command on the connection's session
  (see the per-statement resolution entry below), and one begun inside
  another would discard the open statement's pending writes. `Ping` on such
  a connection succeeds without running a statement; a pooled connection
  reset with a statement still open is reported bad and discarded, so its
  transaction, write-set and row locks never reach the next caller. New:
  `provider.QRDBSession.CommandOpen`.
- **Every SQL surface resolves each table, view and servicer once per
  statement.** In 0.27.0-alpha only the MySQL wire delivered the statement
  boundaries this memoization needs; the PostgreSQL wire (each executed
  statement, and the describe of each parameterized statement at Parse),
  the ad-hoc query service (each request) and the embedded driver (`Exec`,
  `Prepare`, and a `Query` until its rows are closed) now bracket each
  statement as a command too.
- **The SQL point-latency benchmark target is `make bench-sqlpk`** (was
  `make bench-sql-pk` in 0.27.0-alpha), matching its package,
  `test/performance/sqlpk`.

### Removed

- **The row-by-row relational write path.** Every relational write is part
  of a transaction (explicit, or the autocommit statement's implicit one).
  A write on a `*sql.Context` without a QRDB session -- which no SQL surface
  creates -- is refused with `provider.TransactionSessionRequiredError`.
- **BREAKING (Go API): `rowlock.NewOwner`.** An owner is made from the
  session whose lease it uses, `rowlock.NewSession().NewOwner()`; nothing
  outside the package's own tests used an owner with a private lease.

### Fixed

- **`SET autocommit = 0` starts a real transaction.** go-mysql-server marks
  only `BEGIN`/`START TRANSACTION` as an explicit transaction, so with
  autocommit off each relational write committed at once and `ROLLBACK`
  undid nothing. A session with autocommit off now runs one transaction
  until `COMMIT` or `ROLLBACK`, as InnoDB does, and a write to a `kv`,
  entity or collection table inside it is refused with
  `NonTransactionalTableError`, as inside `BEGIN`.
- **Row-lock lease edge cases.** A transaction's first lock request on a
  connection whose lease had ended unnoticed (expired or swept between
  keepalives) failed with 1213 although it held nothing; it now withdraws,
  and retries once on a fresh lease. A lease the session retires (a failed
  release, `Close`, idleness) is marked lost before it is detached, so another
  owner on it can no longer believe it holds a lock the revoke deleted. An
  owner released while its enqueue was in flight deleted nothing and left the
  record on the connection's lease; it now deletes it and fails with
  `OwnerReleasedError`. A lease grant no longer holds the session's lock, and
  of two concurrent grants the loser is revoked. A connection's end revokes
  its lease first and deletes no record one by one -- unless that revoke
  fails, in which case each transaction deletes its own records rather than
  leave them until the lease expires.

## [0.27.0-alpha] - 2026-10-02

Standalone parity (#582) and durable leases. Highlights:

- **Standalone mode serves watch, leases, locks and MVCC** with the same API and semantics as raft mode, through the same engine and the same tenant/database scoping, on all six transports and the SDK. File and objstore backends serve watch and plain transactions; MVCC, leases, locks and revision compares are refused with a typed error there.
- **Leases are durable in both modes.** Lease records and the ID sequence are written in the same apply batch. They are rebuilt on open and snapshot restore, and every recovered lease gets one full TTL. This fixes raft-mode bugs where a restart left leased keys (including lock records) stuck forever, reused lease IDs, and diverged replicas.
- A put naming an unknown lease is refused at apply. A re-put moves a key to its new lease. Keys with a leading `0x00` byte are reserved and refused.

### Added

- **Standalone mode serves watch, lease, lock and MVCC.** A standalone node (the
  daemon's `mode: standalone`, `sdk/go/node.NewStandaloneNode`, and the flat
  `embedded.NewServer`) serves the same API as a raft node, with the same
  semantics and errors, on REST, gRPC, Unix, QUIC, MCP and the embedded
  transport: prefix watch with revision resume and cancellation, lease grant,
  keep-alive, revoke and expiry, locks, and MVCC revisions and historical
  reads. They used to be refused with `requires mode: raft`, which pushed
  hosts onto in-process fallbacks and one-member raft clusters. The families
  are composed over the same engine, lessor and watch hub as raft mode's MVCC
  state machine and resolved through the same per-(tenant, database) registry,
  so tenant confinement is identical. A standalone node runs the lease-expiry
  sweeper itself (`lifecycle.StartLocalLeaseExpiry`), and its leases survive a
  restart like raft mode's. The file and objstore backends keep no revisions
  or leases: they serve watch and value/version transactions, and refuse MVCC,
  lease, lock, revision compares and any lease-carrying write per call with
  `CapabilityUnsupportedError`; an MCP watch there ends with
  `WatchStartRevisionUnsupportedError`. The capability report shows `mvcc`,
  `watch`, `lease` and `lock` true in both modes, and `mvcc.enabled`,
  `watch.enabled` and `lease.enabled` are valid under standalone. The etcd
  listener, cluster membership, rebalance and shard backup stay raft-only.
  `sdk/go/conformance` runs one watch, lease and lock suite against standalone
  and raft nodes on all six transports.

### Fixed

- **The flat embedded server delivers watches.** `embedded.NewServer` built its
  engine without a watch hub, so a watch opened through it never received an
  event; it also composed no watch or lock service and ran no lease expiry.
- **A transaction op naming a lease is refused on the file and objstore
  engines.** The plain KV engine keeps no leases, but its transaction handler
  stored a leased put as an ordinary key that never expired. The whole op set
  is now refused with `CapabilityUnsupportedError` and writes nothing. Plain
  value/version transactions are still served there.
- **A lease revoke whose key delete fails halts the apply on the MVCC engine**,
  as on the Pebble state machine. It was logged and skipped, leaving the key
  with nothing to expire it on that replica alone.
- **A failed standalone apply no longer leaves the lessor ahead of the
  store.** A standalone node keeps serving after a failed apply, so
  `MVCCEngine.Update` now re-reads the committed lease table into the lessor
  when an apply fails (`engine.LessorResyncError` when that read fails too).
- **The Pebble state machine refuses user keys in its reserved `0x00`
  namespace** (`statemachine.ReservedKeyError`), on every write path. A user
  write could overwrite the lease table or the last-applied index.
- **The `qrdb` GUI's embedded server expires its leases.** It gave its MVCC
  engine an anonymous lessor nothing swept, and composed no lock service; it
  now composes the engine through `lifecycle.NewLocalEngine`, sweeps it, and
  serves locks.
- **The lease-expiry sweeper is stopped before the backend closes.**
  `lifecycle.StartLocalLeaseExpiry` returns a stop function that waits for
  the sweeper to exit; the daemon, `node.Node`, `embedded.Server` and the GUI
  binding call it before closing the backend it revokes through.
- **The embedded transport keeps a refusal's type under a lock or an MVCC
  read.** A lease refusal underneath a lock, and `service.MVCCBackendRequiredError`,
  reached embedded callers as untyped errors; they are now
  `transport.CapabilityUnsupportedError`, as on every network transport.

- **Revoking or expiring a lease no longer deletes a key that was written again
  under another lease, or with none.** A key stayed in every lease any of its
  values had named. The MVCC put paths attached the key to the new lease
  without detaching it from the old one, and on the Pebble state machine the
  delete-path detach never ran, because the previous value it read carried no
  lease. Revoking or expiring the old lease then deleted the newer value. A
  service that re-registered under a fresh lease while its previous
  registration's lease was still alive lost the new registration when the old
  lease expired. A key now belongs to the lease its latest write named. A put
  naming another lease moves it, a put naming none (including a plain base-KV
  put) releases it, and every delete releases it. The binding is written in
  the same apply batch as the value, on puts, deletes, batches and
  transactions, on both the Pebble and the MVCC state machine.
  Compare-and-swap still keeps the key's lease. `Revoke` returns its keys in
  key order, so every replica emits the delete events in the same order. On
  the MVCC engine a revoke now retracts the deleted keys' secondary-index
  entries, as any other delete does.
- **Leases survive a restart and a snapshot recovery.** The lessor lived only
  in memory. A restarted node, or a replica recovering from a snapshot, came
  back with no leases: their keys never expired, keep-alive answered
  not-found, and the next grant reused ID 1, so lease IDs diverged across
  replicas. The lease table (each lease's granted TTL, every key binding, and
  the lease-ID sequence) is now persisted under the reserved
  `\x00qrdb.lease/` prefix, carried in snapshots and covered by the replica
  hash. It is restored when a state machine opens or recovers. A restored
  lease starts a full TTL. Leases granted before this release are not in the
  table and do not survive the upgrade's restart, which is what happened to
  them on every restart before.
- **A write naming a lease that no longer exists is refused at apply.** The
  namespaced view checks lease ownership before proposing. A revoke or expiry
  committed between that check and the write let the write through as a key
  bound to no lease, which never expires. The state machine now refuses it with
  the same `service.LeaseNotFoundError` (`NOT_FOUND`) the pre-proposal check
  returns, and a batch or transaction naming one stages none of its
  operations. `pkg/rowlock` relies on that type to report a lost lock. A write naming a lease
  on a state machine built without a lessor is refused as unimplemented
  instead of being written lease-less.
- **`pkg/rowlock`: the deadlock tests no longer race their own requests.** A
  test waited only for the first request's record to be queued, not for that
  request to finish its deadlock check. When the second request completed the
  cycle first, the first request found the cycle too and became the victim,
  failing about one run in thirty under `-race`. The tests now wait until the
  earlier requests are blocked in `wait`, which is entered only after the
  check.

### Changed — BREAKING

- **`embedded.WithStandaloneMode` is removed**, with the registry gate
  (`raftOnlyGatedRegistry`) it mirrored: there is no longer a family a
  standalone node withholds by design. Drop the option. `server.WithStandaloneMode`
  remains and now refuses only the etcd listener.
- **`lifecycle.NewLocalEngine` takes the node's lessor**
  (`NewLocalEngine(backend, stg, pdb, hub, lr)`). It used to build a private
  lessor that nothing swept; pass the lessor the expiry sweeper runs over.

- **`pkg/lease`: mutations take the apply batch.** `Lessor.Grant(w, ttl)` and
  `Lessor.Revoke(w, id)` take a `lease.Writer`. `AttachKey` and `DetachKey` are
  replaced by `Bind(w, key, leaseID)`, where `lease.NoLease` releases the key,
  and `Unbind(w, key)`. `GrantWithID` is removed, since IDs now come from the
  persisted sequence. New: `Restore`, `Has`, `LeaseOf`, `IsTableKey`,
  `TablePrefix`, and the typed `TableWriteError`, `TableReadError` and
  `CorruptTableError`. A state machine opening over a corrupt lease table
  fails with `CorruptTableError` rather than guessing which keys a lease owns.

## [0.26.0-alpha] - 2026-10-01

Merge train 5. Highlights:

- **DROP DATABASE drop fence (#482).** A database drop is atomic with every write: a write that races the drop is refused, not half-applied. Guarded writes travel behind a cluster feature gate and are used only once every node in the cluster supports them. Another tenant's dropping database answers as missing.
- **SQL primary-key statements no longer scale with table size.** The planner now uses a per-table row-count estimate (COUNT(*) stays exact), so a PK point read at 64k rows drops from about 127 ms to about 1.6 ms. Pessimistic row locking holds one lock lease per connection instead of one per statement.
- **Watch** accepts a start revision above current and is no longer cancelled by a racing compaction (#514). KVStore comparability (#66) and the seal guarded-delete arm (#552) are documented.

### Added

- **Cluster feature gate (`pkg/cluster/featuregate`).** A command only newer binaries
  understand is sent anywhere in the cluster only once every node in it -- not just a given
  Raft group's current members -- runs a binary that advertises the feature: a later
  membership change can bring an old binary into a group whose current members all advertise
  today, and go-quicraft catches a rejoining or newly added replica up by replaying the
  group's committed log, which would already hold the command. Each node advertises its
  features in a lease-backed record in the system store
  (`__sys:cluster/features/nodes/{replicaID}`), so a stopped or rolled-back node stops
  counting when its lease expires; a dedicated shard matches its members to records by raft
  address. Once every node advertises a feature it is activated permanently (witnesses
  included), and a node whose binary lacks an activated feature refuses to start with
  `featuregate.UnsupportedFeatureError`. Separately, and before activation too, a replica is
  added to a group only on a node that advertises every feature that group's CURRENT members
  already use -- member add, dynamic membership, scale-up, rebalance and the placement
  scheduler all ask first and are refused with `featuregate.UnsupportedReplicaError` (REST
  409, gRPC/QUIC `FAILED_PRECONDITION`). `qrdbd` withdraws its advertisement on a graceful
  stop; after a crash it lapses with the lease. **After activation, no node may be started or
  added on an older release.** A brand-new `qrdbd` node advertises only once it is a member,
  so after activation it cannot yet be added with `member add`. Guarded writes are the first
  gated feature; standalone nodes and hosts with no transport use them from the start, and a
  single-node cluster activates them at once. A node embedded through the Go SDK cannot refuse
  to start after it is built; it logs the refusal and reports its health as `degraded`. New:
  `service.FeatureGatedHost` and `service.ReplicaAdmitter`,
  `raftbackend.WithFeatureGate` / `NewFeatureGate`, `server.WithFeatureGate` and
  `TenantInfraConfig.FeatureGate`, `placement.WithReplicaFilter`,
  `sdk.Client.StartFeatureGate` and `NewMembershipStoreAt`, and `cluster.Member.Features`.
  **Downstream composers:** a `raftbackend.HostAdapter` over a host with a transport and no
  `WithFeatureGate` now proposes every write UNGUARDED -- no feature is enabled without a
  gate -- so a composer that builds its own adapters must attach the node's gate to each of
  them, or its drop fence falls back to the check made when the database is resolved.
- **Guarded write commands.** A new Raft opcode, `protocol.OpGuarded`, wraps any write
  command with compares (the transaction compare encoding; `version` and `value` targets)
  that the state machine evaluates at the entry's own log position. It is served by the four
  `pkg/statemachine` state machines, `PebbleStateMachine`, `MVCCStateMachine`, `KVEngine` and
  `MVCCEngine`; `SimpleStateMachine` refuses it. A command without the wrapper is
  byte-for-byte unchanged and applies unconditionally. See `docs/databases/engineering.md`.
  A refused guard travels the same typed-kind encoding as every other state-machine
  refusal (`protocol.AppErrorKind`): a new `AppErrorGuardRefused` kind carries the key of
  the compare that failed, decoded ahead of the generic kind dispatch so a refused drop
  fence renders as `database.DroppingError` (409 / `ABORTED`) and any other refused guard
  as `service.WriteGuardRefusedError` (409 / `ABORTED`) through the one `pkg/api/errclass`
  table every transport already renders from -- neither type needed its own per-transport
  mapper. `service.WriteGuardError` (a guard that could not be built -- an unencodable
  compare, a command already guarded) is 400 / `INVALID_ARGUMENT`, the same shape as
  `EncodeRequestError`: raised before anything is proposed.
- `embedded.WithServiceRegistry`: the embedded transport resolves collection calls and SQL
  queries that name a database through the same per-(tenant, database) registry as the
  network transports when the composition configures one. `sdk/go/node` wires its server's
  registry in.

### Changed

- **Breaking:** `TimeSeriesServicer.SetRetentionPolicy` takes a `context.Context` first, so
  a retention change carries the request's database scope like every other write.
- **Breaking:** `database.KeyspaceReclaimer` gains `FenceDatabaseKeyspace` and
  `UnfenceDatabaseKeyspace(ctx, tenantID, database, incarnation)`; every implementation must
  add them.
- **Breaking:** `service.StoreResolver` gains `FenceTarget`, returning a
  `service.FenceStore`; every implementation must add it.
- **Breaking:** `service.ReclaimKV` gains `Put`; every implementation must add it.
- **Embedded collections move to the per-database keyspace.** When a service registry is
  configured, collection calls through the embedded transport that name a database now read
  and write that database's `<tenant>/<db>/` keyspace -- the one the network transports use
  and `DROP DATABASE` reclaims -- instead of the root-keyed layout the embedded transport
  wrote before. Collections an embedder wrote through the old path are not visible through
  the new one; migrate them as for the earlier collection namespace change (export with the
  previous binary, re-import).
- Database metadata records an `incarnation` (see Fixed). Metadata without one reopens with
  an empty incarnation and keeps working.

- **SQL primary-key point statements no longer cost time proportional to the
  table.** go-mysql-server asks a relational table for its row count several
  times while it plans every statement, and `RowCount` answered with an exact,
  linearizable count of the whole table: about 1.7 µs per row, so a
  `SELECT`, `UPDATE` or `DELETE` by primary key took about 110 ms at 64,000
  rows. It now answers from a per-table-generation estimate, counted with a
  local read and refreshed in the background every ten seconds, and reports
  it as inexact. Over the MySQL wire, at 64,000 rows, the median point
  `SELECT` went from 127 ms to 1.6 ms and `UPDATE` from 152 ms to 3.4 ms; see
  `test/performance/SQL_PK_LATENCY.md`. `information_schema.tables`'
  `TABLE_ROWS` and `DATA_LENGTH` report the same estimate, so they may be up
  to about ten seconds stale. `SELECT COUNT(*)` is unaffected and always
  exact: it is no longer answered from the row count, and counts the rows
  instead.
- **A SQL statement resolves each table, view and servicer once.** Table
  lookups within one statement — and every table `information_schema` lists —
  each re-resolved the session's tenant with a linearizable read for each of
  five servicers and re-read the table definition. Statements that cannot
  change a definition (`SELECT`, `INSERT`, `UPDATE`, `DELETE`, `REPLACE`,
  `SHOW`, `DESCRIBE`, `EXPLAIN`) now memoize them for the statement, on
  surfaces that deliver statement boundaries (the MySQL wire). DDL is seen by
  the next statement, as before.
- **Pessimistic autocommit statements share their connection's lock lease.**
  Every locking transaction granted a lease before its first lock and revoked
  it after its last, two Raft proposals on every pessimistic autocommit
  `UPDATE`, `DELETE` and `INSERT`. A connection now holds one lease per
  database it locks in, renewed while it lives and revoked when it ends or
  after ten seconds unused; each transaction deletes its own lock records
  when it ends. A crashed connection's locks still expire with its lease, a
  deadlock victim is still rolled back, and an autocommit statement still
  releases its locks when it ends. New in `pkg/rowlock`: `Session`,
  `NewSession`, `Session.NewOwner`, `Session.Close`.

### Fixed

- **Dynamic membership works on the default `pebble` state machine.** A lease-attached put
  (`MVCCKVService.PutWithLease` / `PutWithLeaseAndMode`) applied on a state machine without
  MVCC revision history and then failed its revision read-back, so `--dynamic-membership`
  on `--sm pebble` failed to register each node and the node did not start. The composer now
  declares a state machine without revision history (`service.WithoutRevisionHistory`,
  `lifecycle.Infra.NoRevisionHistory`, from the state machine's descriptor), and the put
  there reports the key, value and lease it wrote with all revisions `0`, without a
  read-back; on an MVCC state machine it still returns the write's full revision metadata.
  The etcd API refuses to start over a state machine without revision history
  (`etcd.RevisionHistoryRequiredError`), and its `Put` answers `FAILED_PRECONDITION` there
  rather than revision 0.

- **DROP DATABASE is atomic with every write to the database.** Each database now has a
  fence marker in the Raft group holding its data (the system shard, a volume tenant's Host
  shard, or a dedicated-raft database's own shard). While the database is live the marker
  holds the database's incarnation, and every write its views propose carries "the marker
  holds my incarnation" in the same Raft command. A drop fences the database locally, so no
  new handle resolves it, and sets the marker to a tombstone before reclaiming anything. A
  write resolved and proposed before the drop but applied after the fence is refused at
  apply time with `database.DroppingError` (REST 409 / gRPC and QUIC `ABORTED`) instead of
  landing in a keyspace being reclaimed; a write applied before the fence is reclaimed with
  the rest. Because handles are scoped to an incarnation, a handle from before a drop is
  still refused after the name is created again. A failed drop always restores the marker,
  under a context the failed request's cancellation cannot reach, and stays fenced if the
  store cannot be reached. A dedicated-raft database is additionally fenced in its replicated
  placement entry, cleared on a failed drop only once the store marker is confirmed
  restored; a shard cached for a dropped incarnation is evicted and its directory purged
  rather than served, and a never-cached dedicated-raft database can be dropped. Shard IDs
  are never issued twice on a node, dropped ones included (a persisted high-water mark), so
  a re-created dedicated-raft database always gets a new shard. The marker is claimed only
  from absent or the tombstone: creating a name already live on another node is refused
  with `database.AlreadyExistsError` (`Elsewhere` set) rather than taking it over. A name
  this node dropped is refused with `database.ReclaimerRequiredError` by a manager with no
  keyspace reclaimer, and `qrdbd` now provisions its configured databases only after the
  data plane has attached one, so a restart can no longer re-create a dropped name that
  refuses every write. A placement read failure on the fence path surfaces as itself rather
  than as an unknown database, and a collection service cached for a dropped database is
  never reused by the name's next incarnation.
  Vector writes, which are node-local rather than Raft proposals, keep a check made
  immediately before the write.
- **Guarded writes never reach a mixed-version cluster.** A replica running a binary that
  predates guarded writes would refuse one and write nothing while its peers applied it. The
  new cluster feature gate (below) sends a guarded write anywhere only once the feature is
  activated cluster-wide -- every node, not just a given group's current members, so a later
  membership change can never introduce an old binary that the group's log already has the
  command in; until then every group's writes are proposed unguarded and the drop fence is
  checked when the database is resolved, as before guarded writes.
- **The default database cannot be dropped.** Every drop entry point refuses `system` with
  `database.DefaultDatabaseDropError` (REST 400 / gRPC and QUIC `INVALID_ARGUMENT`) before
  touching anything.
- A second concurrent drop of one database is `ABORTED` on QUIC as on gRPC and REST, rather
  than an internal error.
- `SimpleStateMachine` reports a command it cannot decode as a refusal in the result instead
  of the success value, so such a write is no longer acknowledged unwritten.

- **A watch opened at a start revision above current is accepted and stays
  open.** It opens as a live watch that withholds every change below the
  requested revision and delivers from that revision on, matching etcd. It
  was previously cancelled in-band with a compacted error when compaction
  raced past its start revision before the replay goroutine checked, even
  though it never needed a historical scan. The hub now reads the replay
  boundary in the same critical section that registers the watcher, and
  applies the compaction check only to a replay it actually attempted. The
  watch docs listed a 400 / `INVALID_ARGUMENT` refusal for this case; no
  transport ever enforced it, and the docs now describe the accepted
  behaviour.

- **A serialization failure or lock-wait timeout raised during an index
  lookup kept its error code.** `RelationalTable.LookupPartitions` wrapped
  every error in `IndexLookupError`, which hid go-mysql-server's
  `ErrLockDeadlock` (MySQL 1213, "retry") and MySQL's own 1205 / 3572 from
  the client. It was masked until now because planning's exact row count
  usually raised the same stale-definition error first.

### Removed

- `provider.WriteSet.HasBufferedWrites`. Its one caller was `RowCount`, which
  needed it to stop reporting a committed-only count as exact inside a
  transaction; `RowCount` is never exact now.

## [0.25.2-alpha] - 2026-10-01

Security patch. It closes a family of privilege escalations in which a tenant-scoped identity could act on node-wide or cluster-wide resources. The cause was a permission check that compared the caller's tenant with itself on routes that name no tenant.

Node seal and ceremony operations, cluster membership, shard control, rebalance, node unseal shares and backup/restore are now system-tenant only, on every transport. See the BREAKING entries below.

### Security

- **BREAKING: a tenant's Admin or SO can no longer change cluster
  membership.** `admin:add_member`, `admin:remove_member`, `admin:drain` and
  `admin:snapshot` were ScopeSelf, and the built-in Admin and SO roles grant
  them in every tenant. Their routes (`POST /membership/add`,
  `/membership/remove`, `/drain`, `/snapshot`, and the same methods and tools
  on gRPC, Unix, QUIC and MCP) address shards by id and name no tenant, and
  the admin service does no tenant check, so any tenant's Admin could remove
  replicas from the system shard and break quorum for every tenant, or drain
  the node. They are now `ScopeCross`, with `admin:start_shard`,
  `admin:stop_shard`, `admin:shard_state`, `admin:topology` (which enumerates
  every tenant's raft group and node address) and every `rebalance:*`
  permission (`rebalance:execute` moves shards between nodes); no built-in role
  below Owner carries those, but a tenant's SO could grant them to itself
  through a custom role. `admin:peers`, `admin:status`, `admin:health` and
  `admin:capabilities` stay ScopeSelf: the etcd listener's tenant-facing
  `Cluster.MemberList` is authorized with `admin:peers` against the
  database's owning tenant. A tenant tool that drove these operations must run
  as the `system` tenant.
- **BREAKING: shard backup and restore are system-tenant only.** `backup:*`
  was ScopeSelf on routes that name no tenant. A backup is of shards, not of
  a tenant -- the backup service has no tenant model -- so it holds every
  tenant's data on them, and a restore overwrites every tenant's state. No
  built-in role below Owner carries `backup:*`, but a tenant's SO could grant
  it to itself through a custom role and take, or roll back, every tenant's
  data. Every `backup:*` permission is now `ScopeCross`, re-exported as
  `sdk.PermissionBackup{Create,Delete,List,Read,Restore,Verify}`.
- **BREAKING: a tenant's Custodian can no longer submit shares to the node
  barrier.** `barrier:unseal-share` was ScopeSelf and every tenant's built-in
  Custodian role grants it, so a tenant custodian could submit a wrong share
  into an in-progress node unseal and poison the accumulation. The node
  barrier's custodians are the `system` tenant's, enrolled by its Security
  Officer; a tenant custodian's shares go to its own barrier's identity-gated
  routes. It is now `ScopeCross`. Anonymous unseal-share on the boot-time
  sealed-window listener runs no RBAC and is unchanged.
- **BREAKING: a tenant can no longer enrol or list the node barrier's
  Security Officers.** `seal:officer_register` (`POST /v1/barrier/officers`
  and `POST /v1/barrier/recovery/officers`) and `seal:officer_list`
  (`GET /v1/barrier/officers`, `GET /v1/barrier/officers/claim-status`) were
  ScopeSelf. None of these routes names a tenant, so the check compared the
  caller's tenant with itself and admitted every holder: a tenant's SO, which
  holds both through its own tenant's built-in SO role, or a tenant Admin
  granted them by a custom role, could plant officers in the node barrier's
  unseal and recovery domains, and read back the node's officer names and
  certificate thumbprints. Every rekey, revocation and recovery-key generation
  sends a share to every registered officer, so M planted officers could
  reconstruct the node unseal key or mint a root token through the quorum.
  Both permissions are now `ScopeCross`, refused for every non-system identity
  on REST, gRPC, Unix, QUIC, MCP and the embedded `RBACClient`. Anonymous
  first-boot registration on the boot-time sealed-window listener runs no RBAC
  and is unchanged. A tenant role that relied on the gap must move to the
  `system` tenant, or manage its own tenant's officers through
  `tenant_barrier:officer_register` / `tenant_barrier:officer_list`.
- **BREAKING: the rest of the node barrier's lifecycle is system-tenant only
  too.** The same vacuous ScopeSelf covered `seal:bootstrap` (initialize and
  root-token generation), `seal:unseal`, `seal:recover_with_keys`,
  `seal:submit_seal_vote`, `seal:enable_encryption` -- whose body-supplied
  `tenant_id` let a tenant-local holder migrate the node barrier or another
  tenant's; migrating any barrier is a platform-operator act with no
  tenant-side route -- and the node key ceremony's `ceremony:initialize`,
  `ceremony:enroll_custodian` (custodians and external officers, who receive
  node shares), `ceremony:revoke_custodian` and `ceremony:sign_csr` (which
  signs with the cluster CA). All are now `ScopeCross`. The built-in SO, Admin
  and Operator roles of a non-system tenant keep the grants and are refused
  them; the same roles in the `system` tenant are unaffected. `seal:status`,
  `ceremony:status` and the share-claim verbs stay ScopeSelf.
- The Go SDK re-exports every new cross-scope permission
  (`sdk.PermissionSealOfficerRegister`, `sdk.PermissionSealOfficerList`,
  `sdk.PermissionSealBootstrap`, `sdk.PermissionSealUnseal`,
  `sdk.PermissionSealRecoverWithKeys`, `sdk.PermissionSealEnableEncryption`,
  `sdk.PermissionSealSubmitSealVote`, `sdk.PermissionCeremonyInitialize`,
  `sdk.PermissionCeremonyEnrollCustodian`,
  `sdk.PermissionCeremonyRevokeCustodian`, `sdk.PermissionCeremonySignCSR`,
  `sdk.PermissionBarrierUnsealShare`, `sdk.PermissionAdminDrain`,
  `sdk.PermissionAdminSnapshot`, `sdk.PermissionAdminStartShard`,
  `sdk.PermissionAdminStopShard`, `sdk.PermissionAdminShardState`,
  `sdk.PermissionAdminTopology` and `sdk.PermissionRebalance{Execute,Plan,Report,Stats}`;
  `sdk.PermissionAdminAddMember` and `sdk.PermissionAdminRemoveMember` already
  existed and are now cross-scope).

## [0.25.1-alpha] - 2026-10-01

Fixes clustered qrdbd under system barrier protection `none`, which could not start on go-quicraft 0.15; encrypts the SDK clustered node's Raft WAL; and lets a host supply the `/v1/ca/bootstrap` bundle instead of colliding with the route. BREAKING (on disk): a sealed SDK clustered node refuses a WAL written by an earlier release; start it from an empty WAL directory.

### Changed — BREAKING

- **The SDK clustered node encrypts its Raft WAL, through the same code path
  as qrdbd.** `node.NewClusteredNode` built its host `WithoutEncryption`
  whatever the seal, so every committed key and value sat in cleartext in the
  WAL until compaction. qrdbd and the SDK node now both configure the WAL with
  `database.NewRaftWAL`:
  - seal disabled: `WithoutEncryption`;
  - a single member, or system protection `none`: go-quicraft's own WAL
    `WithBarrier`, which encrypts once encryption is enabled;
  - a cluster whose system barrier encrypts: the multiplex (shared
    system-barrier WAL plus per-tenant dedicated WALs).

  The SDK node now hands the multiplex to its tenant infrastructure, so
  `dedicatedWal` databases get their own tenant-barrier WAL as on qrdbd. The
  WAL directory records its format in `.qrdb-wal-format`. A WAL in the other
  format, including an SDK node's unmarked pre-0.25.1 plaintext WAL under a
  barrier, is refused with `*database.RaftWALFormatError` and never converted.
  Start such a node from an empty WAL directory; a replica re-syncs from its
  peers. qrdbd's own unmarked WALs are adopted, because its format did not
  change.

### Added

- **`sdk.WithBootstrapCABundle` (`server.WithBootstrapCABundle`): a host
  supplies the `/v1/ca/bootstrap` bundle and qrdb serves the one route.** The
  provider, `func(ctx) ([]byte, error)`, is read per request, and only its
  CERTIFICATE blocks are served. Configuring it together with qrdb's cluster
  CA (`WithCABootstrap`) refuses startup with `*sdk.CABootstrapConflictError`.

### Fixed

- **`/v1/ca/bootstrap` is no longer claimed when nothing is served there.**
  The REST server reserved the path whether or not a bundle was configured, so
  a host serving its own first-contact bundle there (go-xkms's xkmsd)
  panicked at boot with a route collision. With no source configured the path
  is not registered at all.
- **A cluster booting under system protection `none` starts again.**
  go-quicraft 0.15 accepts a caller-supplied LogDB beside `WithBarrier` only
  if it declares itself encrypted at rest. The multiplex's shared waldb
  truthfully reports unprotected while the system barrier is in plaintext
  mode, so every node of such a cluster was refused at host creation. Such a
  node now takes go-quicraft's own WAL under the barrier, which is the same
  directory and format. Dedicated-WAL shards use the shared WAL until the
  next boot after encryption is enabled. `TestClusterEnableEncryption_SystemScope_RF3`
  and `_TenantScope_RF3` pass again.
- **The auth CLI integration test assigns the auditor role by id.** `--roles`
  takes role ids. The fixture passed the name `auditor`, which role-reference
  validation correctly refuses. This was a test defect, not replication: the
  role was seeded under its derived id.

## [0.25.0-alpha] - 2026-10-01

Adds the per-tenant DEK keyring on every transport, bearer authenticators on gRPC and Unix, and a sealed-window surface hosts mount. BREAKING: the unused cluster-DEK and tenant-DEK-manager APIs are removed (see Changed — BREAKING), and `qrdbd`'s `<data-dir>/dek` directory is no longer read and may be deleted.

### Changed — BREAKING

- **`transport.DEKClient` is the tenant keyring and nothing else.** The
  cluster-DEK verbs (`DEKInitialize`, `DEKGetStatus`, `DEKRotate`,
  `DEKGetCurrent`, `DEKSyncToNode`) and `TenantDEKGetOrCreate`,
  `TenantDEKDelete`, `TenantDEKList` and `TenantDEKGetStatus` are removed with
  their request/response types; `TenantDEKRotate` now returns the keyring
  status. None of them worked against a server: no transport served them, the
  gRPC, QUIC and Unix clients were stubs, and the REST and MCP clients called
  routes and tools that did not exist. `service.DEKServicer` is rewritten to
  match.

- **The old tenant-DEK hierarchy is deleted.** `dek.ClusterDEKManager` held its
  key in process memory only, was never initialised by the daemon, was never
  persisted and discarded the old key on rotation; `dek.TenantDEKManager`
  stored keys wrapped by it in a node-local Pebble directory, so a restart or a
  second node could read none of them. Removed with them:
  `seal.TenantKeyManager` (no production caller) and `sdk.TenantKeyManager`,
  the `sdk/go/dek` package, `server.WithTenantDEKManager`,
  `lifecycle.Infra.TenantDEKManager`, and `qrdbd`'s `<data-dir>/dek` store,
  which is no longer opened and may be deleted.

- **Tenant teardown has no DEK-shred step.** `tenant.DEKShredder`,
  `TeardownWiring.SetDEKShredder`, `tenant.WithDEKShredder`,
  `tenant.DEKShredError` and their `sdk` re-exports are removed. The keyring is
  a record in the tenant's own store, so it is unusable from the fence on and
  is removed by the barrier destroy and the keyspace reclaim. The order is now
  fence, destroy barrier, reclaim keyspace; persisting the retirement before
  anything irreversible (#483) is unchanged.

### Added

- **Per-tenant data encryption keyring, served on every transport.** A tenant
  can now encrypt and decrypt its own data under a versioned AES-256-GCM key
  that never leaves the server — `TenantDEKEncrypt`, `TenantDEKDecrypt`,
  `TenantDEKRewrap`, `TenantDEKRotate`, `TenantDEKRetire` and
  `TenantDEKStatus` on REST (`/v1/dek/*`), gRPC and Unix, QUIC (`dek.*`), MCP
  (`qrdb_dek_*`), the embedded transport and `Client.DEK()`, plus a
  `qrdb dek` command group. See `docs/tenant-dek/`.

  The keyring is one record in the tenant's own resolved store, so it is
  wrapped by the tenant's barrier (a dedicated-isolation tenant) or the system
  barrier (a prefix tenant), replicated through the system shard, durable
  across restarts, unreachable while the tenant is sealed and gone with the
  tenant. Rotation keeps old versions decrypting; rewrap moves a ciphertext to
  the current version without returning its plaintext; retire destroys an old
  version and refuses the current one. Ciphertext carries its key version in
  an internal header and binds the header, the tenant and caller-supplied
  associated data into the AEAD.

  Every call acts on the caller's own tenant. A request naming another tenant
  is refused with PermissionDenied / 403, from the system tenant too.

  Six new `ScopeSelf` permissions: `dek:encrypt`, `dek:decrypt`, `dek:rewrap`
  (crypto-use, denied to the Security Officer), `dek:rotate`, `dek:retire`,
  `dek:status`. Built-in grants: User — encrypt, decrypt; Operator — encrypt,
  decrypt, rewrap, status; Admin — rotate, rewrap, retire, status.

  A tenant on barrier protection `none` keeps its keyring record without
  at-rest encryption, exactly as it keeps the rest of its data; encrypt and
  decrypt still work.

- **Embedder bearer authenticators work on gRPC and Unix, and may declare
  their login method unobservable.** The gRPC and Unix auth interceptors now
  carry the caller's `authorization` metadata into the same context carrier
  REST and MCP fill from the `Authorization` header
  (`auth.HTTPAuthorizationFromContext`), so an authenticator registered with
  `node.WithAuthenticators` sees the credential on every one of them. The
  carrier and the decline sentinel are re-exported as
  `sdk.HTTPAuthorizationFromContext` and `sdk.ErrUnauthenticated`, so such an
  authenticator is written against the SDK alone. An
  authenticator whose tokens are minted by the host after its own login
  ceremony implements the new `auth.UnobservableMethodAuthenticator`
  (`sdk.UnobservableMethodAuthenticator`). When it reports true, the chain
  skips the account's permitted-method check, as it does for a capability
  module's session token, and the host enforces the policy at token issue.
  Every other step still runs. Native QUIC frames carry no credential, so a
  bearer does not authenticate there; go-quicbaas's QUIC is HTTP/3 REST and
  carries the header.

- **Hosts mount qrdb's sealed surface instead of copying it.**
  `sdk.NewSealedWindowHandler(barrier, ceremony, credential, probe)` returns
  the whole sealed allowlist as an `http.Handler` behind qrdb's sealed gate:
  barrier status, initialize and unseal, Shamir and recovery unseal, officer
  registration and share claims, the ceremony verbs and the root token.
  `GET /health` answers the sealed envelope.
  `sdk.SealedWindowMCPTools(...)` returns the matching MCP tools, each already
  gated and error-coded. `sdk.NewSealedWindowListenerProbe(barrier, ceremony)`
  builds the one probe every mounted transport shares. qrdb's own boot-time
  listener is built from the same constructors
  (`rest.NewSealedWindowServer`, `mcp.NewSealedWindowServer`), so there is
  one implementation. `qrdb_health` answers `sealed` on a window with no
  admin service, as gRPC `Health` does. See docs/seal/sealed-window.md.

### Fixed

- **Every transport refuses a standalone node's raft-only families with one
  typed error.** MVCC, watch, lease and lock now answer
  `*transport.CapabilityUnsupportedError` naming `requires mode: raft` on
  REST, gRPC, Unix, QUIC, MCP and the embedded transport alike. Before this:
  the embedded transport answered `*embedded.NilServiceError` (the error
  reserved for a wiring defect; it still is, via the new
  `embedded.WithStandaloneMode` set by `node.NewStandaloneNode`); REST
  registered the lease and lock routes only when a service was wired and
  answered a router 404, now it attaches them and answers 501; MCP did not
  register the MVCC, watch, lease and lock tools and answered "tool not
  found", now it registers them and answers `UNIMPLEMENTED`. Watch refusals
  on gRPC, Unix, QUIC and MCP arrive as the stream's terminal response, whose
  `Err()` is the same typed error; MCP's watch poll no longer re-polls the
  refusal forever. `sdk/go/conformance/standalone_refusal_test.go` pins every
  transport.

- **`node.Client().Lock()` works on a raft node.** The lock service was never
  passed to the in-process transport, so the lock family answered a
  wiring-defect error in the one mode that serves it.

- **Deleting a dedicated-isolation tenant now reclaims its records in the
  shared store.** Its identity records live under
  `seal.TenantSharedNamespace(tenant)` (`__tiam:<tenant>/`), which
  `service.TenantKeyPrefixes` did not cover, so they survived the delete —
  as ciphertext under a destroyed barrier, or as plaintext under barrier
  protection `none`. The reclaim now covers the range.

- **`make fmt` and `make lint` pass together again.** Six test doubles
  returned two composite literals in one `return`. go1.27's `gofmt` and
  golangci-lint's gofmt indent that form oppositely, so the tree failed one
  check or the other. They now return named values, which both formatters
  accept. The change is to test code only.

- **A database-scoped embedded transport keeps the standalone refusal.**
  `Client.DatabaseScoped` on a standalone node dropped the standalone flag,
  so the raft-only families fell back to `NilServiceError` there.

## [0.24.0-alpha] - 2026-10-01

Merge train 3. This release contains BREAKING changes; see the "Changed — BREAKING" entries below before upgrading. Highlights:

- **Optimistic or pessimistic SQL concurrency.** A per-database `sql.concurrency` setting and a `SET qrdb_concurrency` session override choose between them. Row locks are replicated and lease-bound, and requests wait in FIFO order. Deadlock detection, lock-wait timeouts, `SELECT … FOR UPDATE` / `FOR SHARE` and `NOWAIT` follow InnoDB semantics, and a failed statement inside a transaction is undone as a whole. See docs/row-locking/.
- **Security hardening across every transport:**
  - per-database barriers only on the data path; sealed databases fail closed and their open streams end;
  - etcd confinement;
  - tenant-owned SQL CREATE/DROP;
  - RAG confinement;
  - `databases:create_raft` / `create_volume`;
  - lease ownership on writes;
  - the boot-time sealed window refuses requests after unseal;
  - 0-RTT refused on QUIC and HTTP/3;
  - the SO PIN is held in protected memory and sent only over TLS;
  - node-barrier SO operations are system-tenant only;
  - route checks read the same request values as the handlers.
- **Identity:** role bindings and attributes take effect, and there is one source of truth for factors.
- **SDK surface:**
  - auth-method tokens;
  - typed atomic-write refusal;
  - MCP tool-error contract;
  - per-node auth-method vocabulary;
  - the SO PIN set with `WithSOPIN` covers every SO-gated call on every transport.
- **Process hardening** against same-user memory reads at start.
- **SQL sequences:** an epoch fence for sequence blocks; MySQL `TRUNCATE` resets `AUTO_INCREMENT`.
- **Release governance:**
  - the Release workflow is the only tag creator;
  - VERSION is authoritative;
  - the public-releasability gate loads its patterns from outside the tree.
- **Test reliability:**
  - a cross-process port lock;
  - the bootstrap-admin guard race is fixed;
  - stale integration tests are corrected.

### Changed — BREAKING

- **New interface methods.** `service.DatabaseServicer` gains `UpdateSQL` and
  `UpdateSQLForTenant`, and the Go SDK's `transport.DatabaseClient` interface
  gains `UpdateDatabaseSQL` (which `transport.TenantDatabaseClient` forwards).
  Every
  implementation outside this repository — a composer's own service, a test
  double, a custom transport — must add them to keep compiling.
- **TypeScript SDK (`@qrdb/sdk`): the wire-protocol names now match the Go
  SDK, and the old names carry the SQL settings.** `DatabaseClient.updateSQL`
  is renamed `updateProtocol`, `DatabaseSQLConfig` (`{mysql, postgresql}`) is
  renamed `DatabaseProtocolConfig` — including the type of
  `DatabaseInfo.protocol` and `CreateDatabaseRequest.protocol` — and the old
  `UpdateDatabaseSQLRequest` is `UpdateDatabaseProtocolRequest`. The freed names
  are the per-database SQL settings: `updateSQL(name, {concurrency})`
  (`PATCH /v1/databases/{name}/sql`; omit the settings to restore the
  defaults), `DatabaseSQLConfig {concurrency?: 'optimistic' | 'pessimistic'}`
  and an `sql` field on `DatabaseInfo` and `CreateDatabaseRequest`. A caller
  passing `{mysql, postgresql}` to `updateSQL` no longer type-checks; rename the
  call to `updateProtocol`. The desktop client's binding call is fixed with it:
  the protocol toggle invoked a `DatabaseBinding.UpdateSQL` the binding did not
  have.
- **MySQL error 1235 (`ER_NOT_SUPPORTED_YET`) is classified `Unimplemented`**
  by `pkg/api/errclass` (501 / `UNIMPLEMENTED` / `StatusUnimplemented`). It
  was unclassified, so each transport answered with its internal-error
  fallback. `SKIP LOCKED` is what raises it.
- **PostgreSQL wire: `SET qrdb_<name>` reaches the engine.** It was accepted
  and ignored, whatever the name; an unknown `qrdb_*` setting, or an invalid
  value for a known one, is now an error. `SET LOCAL lock_timeout` and `SET
  LOCAL qrdb_*` are refused with SQLSTATE 0A000 instead of succeeding as a
  no-op.
- **`SET GLOBAL innodb_lock_wait_timeout` is refused**; the variable is
  session-scoped (see Added).
- **A per-database barrier is accepted only where it is on the database's data
  path, so a non-system tenant can no longer create one.** Until this release a
  barrier was accepted on a `backend: raft` database and on any database of a
  volume- or raft-isolated tenant, although none of those is served from the
  store the barrier wraps — the barrier was decorative. Those requests now fail
  with `database.BarrierOffDataPathError` (`422` / `FAILED_PRECONDITION`). The
  one remaining shape is a non-raft database with an explicit `--data-dir`,
  which only the system tenant can set, so until per-database storage bindings
  land (they restore per-database barriers for every tenant and backend, with
  the barrier on the data path) tenants cannot have one. A barrier-enabled
  record already on disk -- a raft database's, or any database's of a volume-
  or raft-isolated tenant -- still opens, sealed, fails closed until unsealed,
  and is named in a startup warning.
- **`service.BarrierSealedError` names the sealed database in `Database`.** A
  refusal because a database's own barrier is sealed now carries the database
  in `Database` and reads `barrier sealed for database: <name> (tenant: <id>)`;
  `TenantID` alone, and the `barrier sealed for tenant: <id>` text, now mean
  the tenant's barrier. A client that took every `BarrierSealedError` to be the
  tenant's must check `Database` first.
- **etcd listeners need TLS, the node's authentication and a client
  certificate.** A listener no longer starts without TLS and authentication; a
  client without a certificate, or from a tenant other than the database's
  owner, is refused. Only the mapped RPCs are served: `Auth.*`, member
  changes, `Defragment`, `Snapshot`, `Hash`, `HashKV`, `MoveLeader` and
  `Downgrade` are refused with `PERMISSION_DENIED` (previously stubs, no-ops or
  `Unimplemented`). Keys are stored under the database's
  `<tenant>/<database>/` namespace rather than at the shard root, so data
  written through an etcd listener before this release is not visible through
  it; export it with the previous binary and re-import it. `Compact` on the
  shared shard is reserved to the system tenant, as on every other transport.
- **SQL `CREATE DATABASE` / `DROP DATABASE` now require `databases:create` /
  `databases:delete`**, and a tenant session creates a database its tenant
  owns (counted against its quota) and can drop only its own. A role that
  relied on reaching either statement without those grants, or a tenant that
  relied on dropping a database it does not own, is refused.
- **RAG needs the node's database registry.** It is bound by the server at
  start; a server composed without one refuses RAG with
  `service.CapabilityUnsupportedError`. `qrdbd`, `sdk/go/node` and the
  `node://` driver all compose one.
  `service.NewScopedRAGStore` takes a `*service.RAGBinding`.
- **`service.NewRAGService` takes a `*service.ScopedRAGStore`** instead of a
  `KVServicer`. RAG documents ingested before this release sit at the store
  root and are no longer read; re-ingest them.
- **`database.TenantAccessDeniedError` is removed.** Nothing returns it: a
  tenant addressing another tenant's database now gets
  `database.NotFoundError` (`404` / `NOT_FOUND`) instead of `403` /
  `PERMISSION_DENIED`. A SQL session sees only its own tenant's databases in
  `HasDatabase` and `USE`, as it already did in `SHOW DATABASES`.
- **`wal` and `nodehost` are no longer valid database names.** Creating one
  fails with `database.ReservedNameError` (`400` / `INVALID_ARGUMENT`). A
  database of either name created before this release still opens.
- **`Client.DatabaseScoped` returns `(*Client, error)`** and
  `transport.DatabaseScoper.WithDatabase` returns `(Transport, error)`, so a
  transport that cannot scope refuses instead of returning the unscoped
  client. Scoping to `""` or `system` returns the receiver.
- **Creating a raft-backed or volume-isolated database needs its provisioning
  permission.** A role holding `databases:create` but not
  `databases:create_raft` can no longer create a `backend: raft` database or
  any database of a raft-isolated tenant; one without `databases:create_volume`
  can no longer create a database of a volume-isolated tenant. The built-in
  roles that grant database management hold both.
- **etcd is served only by an `engine: kv` database on a node in mode
  `raft`.** Enabling etcd on a timeseries or vector database is refused at
  create and on update with `database.EtcdEngineUnsupportedError` (`400` /
  `INVALID_ARGUMENT`); it would have served that engine's internal keys. A
  standalone node's etcd listener refuses to start
  (`service.CapabilityUnsupportedError`, "requires mode: raft"): MVCC, leases
  and watches are raft-only on every other transport, and the listener used to
  serve them anyway. `backend: raft` is not required -- the documentation
  said it was, and nothing checked it.
- **The sealed-window allowlist is trimmed to verbs that are justified as
  anonymous.** Officer list, rekey, recovery-key generate / delete / status,
  external officer enrollment (`/v1/init/enroll-external-officer`,
  `CeremonyEnrollExternalOfficer`), and the `/v1/init/` and `/v1/bootstrap/`
  REST prefixes are no longer reachable while the barrier is sealed, on any
  transport: they answer `503` / `UNAVAILABLE`. Rekey and the recovery-key
  verbs already refused while sealed. Use `barrier officer claim-status`
  during first boot, and `barrier officer list` once the node is unsealed.
  The REST allowlist can now hold method-qualified entries (`POST
  /v1/barrier/officers`), so a composer matching entries itself must use
  `sealedwindow.RESTEntryMatches`.
- **The boot-time sealed-window listener refuses every request once the
  barrier is unsealed**, with `503` and `{"code":"SEALED_WINDOW_CLOSED"}` on
  REST, and `UNAVAILABLE` with `sealed-window listener closed: barrier
  unsealed` on gRPC, Unix, QUIC and MCP. A client polling it across the unseal
  retries against the regular listener. It no longer starts the MySQL or
  PostgreSQL wire protocols, which carry no sealed-window verb.
- Writing `roleIDs` (`CreateUser`, `UpdateUser`) or `roleBindings` or `attributes`
  (`UpdateUser`) requires `roles:assign` in the user's tenant, and in the tenant of every
  binding written, on every transport including embedded; `AssignRole` and `UnassignRole` go
  through the same gate (`roles:assign` / `roles:unassign` in the user's tenant, and an assigned
  role is validated like any other). Every referenced role must exist
  and belong to that tenant itself -- the system tenant's built-in roles are not assignable in
  other tenants, which seed their own (400 / `INVALID_ARGUMENT`) -- whether or not RBAC is enabled; a node
  with no role service refuses the write (501 / `UNIMPLEMENTED`).
- User attributes reach ABAC policies only as `subject.user_attrs.<key>` (match key
  `user_attrs.<key>`), never as the authenticated `subject.attrs.<key>`, so an attribute
  cannot forge a certificate- or token-derived value.
- `User.Factors` is removed. Factors and MFA state are read from the auth-factor store once
  per response; if that read fails the request fails with 503 / `UNAVAILABLE` instead of
  reporting no second factor.
- **`service.SequenceServicer` carries an epoch** (#547).
  `Allocate` returns `(first, epoch, err)`, and a new `Epoch` method reads a
  sequence's current epoch. `Reset`, and a MySQL-dialect `TRUNCATE TABLE`'s
  sequence reset, advance the epoch in the same compare-and-swap that zeroes
  the counter. Implementations and callers outside this module must follow
  the new signature. The stored value grows from 8 to 16 bytes, counter
  first; a counter-only value written by an earlier release is read as that
  counter at epoch 0, so no sequence restarts on upgrade.
- **Upgrade note: finish upgrading every node before running
  `ALTER TABLE ... AUTO_INCREMENT = 0` or a MySQL `TRUNCATE TABLE`** (#547).
  During a mixed-version window, a node still on an earlier release rewrites
  a sequence's stored value as 8 bytes whenever it allocates, which resets
  its epoch to 0. A later reset can then bring the epoch back to a value
  that a block reserved before the first reset still carries, and that block
  would be accepted as current.
- **An embedded create is tenant-scoped and provisioning-checked.** The
  embedded transport's `CreateDatabase` called the system-scoped `Create`; it
  now creates through `CreateForTenant` for the context's tenant (owned by it,
  inside its quota; a context with no tenant creates for the system tenant).
  On an `sdk/go/node` node with request authentication, a raft-backed or
  volume-isolated create through it needs an identity in the context holding
  `databases:create_raft` / `databases:create_volume`, as on every other
  transport.
- A `roleBindings` entry on a user outside the system tenant must name that user's own tenant;
  any other is refused with `user.RoleBindingTenantError` (400 / `INVALID_ARGUMENT`), because
  such a user is refused every request on another tenant before a binding is consulted and the
  binding could never grant. A system-tenant user may hold bindings in any tenant. A binding's
  tenant is stored normalized, so an empty tenant is stored as `system`.
- MCP user records render `certBindings[].kind` as its string label (`spki_thumbprint`,
  `full_cert_fingerprint`, `piv_attested`) instead of the numeric ordinal, matching REST, gRPC
  and QUIC, and omit `certBindings[].notAfter` when the binding has no expiry instead of
  sending the zero time.

### Added

- `rest.SealedWindowListenerMiddleware`,
  `grpc.SealedWindowListenerUnaryInterceptor` /
  `SealedWindowListenerStreamInterceptor` (re-exported from the Go SDK), a
  `WithSealedWindowListener` option on the REST, gRPC, Unix, QUIC and MCP
  servers, and `sealedwindow.Decide` / `RESTEntryMatches`: the gate of a
  boot-time sealed-window listener, which refuses every request once the
  barrier is unsealed and anonymous officer registration once enrollment has
  closed (`sealedwindow.ListenerProbe`, re-exported as
  `sdk.SealedWindowListenerProbe`).
- `ceremony.CeremonyService.EnrollmentOpen`,
  `seal.StorageBarrier.MarkCeremonyOperational` / `CeremonyOperational` /
  `Initialized`.
- `forward.ClusterPeer` and `forward.MaxBodyBytes`, the cluster-node check
  and the body bound a receiver applies to a forwarded on-behalf request
  before reading its body.
- `ceremony.SOPINLockedError` and `service.SignCSRThrottledError`.
- `sealedwindow.WithAnonymous` / `Anonymous`, the mark the boot-time
  sealed-window listener sets on every request it admits.
- `pkg/authfactors/authfactorstest`: a software `authfactors.Factor` for tests
  that drive the real auth-factor service, with a strict variant that
  verifies a possession proof over each nonce and a permissive one. Imported
  by tests only.
- `sdk.TenantFromClientCert` (and `sdk.ErrInvalidClientCertificate`): qrdb's
  certificate-to-tenant rule, extracted from
  `MTLSAuthenticator.TenantFromCert` into `auth.TenantFromClientCert` and
  re-exported, so a composing host can resolve a verified certificate's user
  before any tenant is bound: derive the tenant, bind it with
  `sdk.ContextWithTenant(ctx, sdk.NormalizeTenantID(t))`, then call
  `GetUserByCertThumbprint` within it. The authenticator delegates to the same
  function, so there is one rule. No cross-tenant thumbprint index is added.
  `pinnedTrustDomain == ""` accepts any trust domain and is for tests only;
  composers must pin.
- `sdk.WithSOPIN(ctx, pin) (ctx, release, err)`: the Go SDK presents the SO
  dual-factor PIN on every network transport -- the `X-QRDB-SO-PIN` header on
  REST and MCP, `x-qrdb-so-pin` metadata on gRPC and the Unix socket, the
  request's `so_pin` field on QUIC. The PIN is sealed in protected memory,
  never logged, and destroyed by `release`; per-request plaintext copies are
  wiped once written. `err` is a protected-memory failure sealing it. Every
  network transport also fills the request's own `so_pin` field
  (`BarrierSeal`, `BarrierInitializeShamir`, `BarrierRekey`,
  `BarrierGenerateRecoveryKeys`, `CeremonyInitialize`, `CeremonySignCSR`) from
  the bound PIN when the request carries none, because the server reads that
  field too; a caller never sets `SOPin` by hand. A request whose own `SOPin`
  differs is refused with `sdk.SOPINConflictError` on every transport.
- Every transport's user response carries `roleBindings`, `attributes`, the metadata of the
  account's enrolled auth `factors` (never their credential data), and `mfaEnabled`,
  `mfaMethods` and `mfaEnrolledAt` derived from those factors. `UpdateUser` sets or clears
  `roleBindings` and `attributes` on every transport.
- A role binding grants its role in authorization checks that target the binding's tenant,
  and nowhere else. REST, gRPC, unix, QUIC and MCP resolve the tenant a request acts on — the
  tenant of the user or role it names, or the tenant a per-tenant barrier operation names —
  before the route-level permission check and authorize against it, so a binding for tenant A
  works on every route that acts on A. Tenant administration (a tenant's record, quotas and
  entitlements) stays checked against the caller's own tenant. A tenant named in a request body
  or payload is read by decoding it into the handler's own request type, so the check authorizes
  exactly the value the handler acts on; a body that does not decode is refused with 400 (QUIC
  `StatusInvalidArgument`) and one over 1 MiB with 413. MCP reads an id argument the way its tools
  do, a JSON number or a string. The FIPS 140-3 Security Officer deny-list and strict separation of duties
  apply to bound roles as they do to held ones.
- `embedded.WithAuthMiddleware`; `sdk/go/node` hands the embedded transport the same
  `AuthMiddleware` its network transports enforce with. The SDK re-exports
  `RoleNotAssignableError`, `RoleServiceRequiredError`, `RoleBindingTenantError` and
  `UserFactorLookupError`.
- **The permitted-authentication-method rule is exported.**
  `CheckAuthMethod(user, method)` in `pkg/auth/middleware` is the function the
  authentication gate runs, re-exported as `sdk.CheckAuthMethod` together with
  `sdk.AuthMethodNotPermittedError`, so a composing host that runs its own login
  ceremony enforces `User.AuthMethods` without restating the rule. The method
  tokens gain `auth.MethodPIV` (`"piv"`) and `auth.MethodWebAuthn`
  (`"webauthn"`) beside `MethodMTLS`, each re-exported by the SDK.
- **Every cross-scope permission and the RBAC denial type cross the SDK
  boundary.** `sdk/go/types_permission_cross.go` names each `ScopeCross`
  permission (`sdk.PermissionAuditReadCross`, the tenants, volume-pool,
  registration-config and credentials families, `sdk.PermissionMVCCCompact`,
  and the node-barrier SO permissions `sdk.PermissionSealSeal`,
  `sdk.PermissionSealRekey`, `sdk.PermissionSealRotateRootKey`,
  `sdk.PermissionSealGenerateRecovery` and `sdk.PermissionSealInitializeShamir`,
  which are now system-tenant only), and a test fails when the registry holds one the SDK does not name.
  `sdk.PermissionDeniedError` aliases `rbac.PermissionDeniedError`, so a
  consumer tells a denial apart from an authorizer that failed to decide.
- **`SaveIfAbsent`'s refusal is typed and crosses the SDK boundary.**
  `dao.AtomicWriteUnsupportedError` (entity type, plus the store's refusal when
  there is one) is re-exported as `sdk.DAOAtomicWriteUnsupportedError`. It
  matches `sdk.ErrKVAtomicWriteUnsupported`, so an SDK consumer fails closed on
  a non-atomic store without importing `pkg/dao`.
- **The MCP tool-error contract is exported through the SDK.**
  `sdk/go/types_mcp_wire.go` re-exports `pkg/api/mcp/wire`: `sdk.MCPCode` and
  its seventeen constants, `sdk.MCPErrorPayload`, `sdk.MCPErrorEnvelope`,
  `sdk.NewMCPErrorEnvelope`, `sdk.DecodeMCPErrorPayload` and `sdk.MCPCodes`. A
  module's MCP tools already receive the classified envelope from the server's
  error-code middleware when they return an error; a test now pins that.
- **A capability module can observe connections on every listener.**
  `apimodule.ConnObserver` (`ConnOpened` / `ConnClosed`, with the
  `apimodule.Transport` the connection arrived on) is an optional
  `ServiceModule` extension; the unified server reports each connection
  accepted on REST (TCP and HTTP/3), gRPC, unix, native QUIC and MCP exactly
  once opened and once closed. A composing host keeps its own active-connection
  metrics without a parallel listener. Re-exported as `sdk.ModuleConnObserver`,
  `sdk.ModuleTransport` and `sdk.ModuleTransport*`. A server whose modules do
  not implement it wraps no listener.
- **The secure analyzer's rule 4: an unborrowed `secure.Buffer.Bytes` read
  must say why** (#428). `Bytes` holds no lock once it returns, so a
  concurrent destroy can unmap the pages mid-read. Every reference outside a
  `_test.go` file now needs a `//secure:unborrowed <injected-interface|allocates-protected|nested-enclave> <reason>`
  directive trailing the read or standing alone on the line above it; `make secure-analyzer` (CI's Lint
  job) fails without it, and `make test-secure-analyzer` plants a bare read to
  prove the gate goes red. The eleven reads left on `main` each reach code rule 6 keeps out of a borrow
  (a consumer-supplied interface, or a protected allocation) and are marked: the cluster DEK wrap (2, the
  subject of the protected-barrier design in #145) and nine in the Shamir
  unseal, recovery, root-token and custodian paths that the share-holder
  rewrite replaces. The `SecretValue.WithBytes` and nested-enclave reads the
  issue listed were already converted by the go-enclave adoption.
- **`securecopy`: the secure analyzer now checks lifetimes, not only
  declarations** (#459). A copy of protected bytes into ordinary memory
  (`append`, `copy`, `bytes.Clone`, `string(...)`, or a call that returns one)
  must be wiped or consumed after it is made, or returned to a caller that
  then owns it. Consumers and copy-returners are inferred across packages from
  go-enclave's `Wipe`/`NewSealed`; `//secure:consumes` declares a contract
  inference cannot see, and `//secure:unwiped <reason>` justifies a copy at
  its site. A `defer secure.Wipe(x)` written before the copy is reported, since
  it wipes what `x` held when the defer ran. It runs in the same
  `make secure-analyzer` sweep, and the self-test plants the unwiped copy-out
  that motivated it. 24 copy-outs were measured; 2 carry `//secure:unwiped`.
  What it cannot see (path sensitivity, aliases, interface dispatch, views
  handed to callees) is listed in `docs/barrier-seal/engineering.md`.
- **`make check-public-releasable`, a whole-tree publication gate, runs in the
  Lint job.** Every tracked file publishes, so the gate rejects any tracked
  file that names the private development fleet: an address on one of its
  subnets, one of its host names, or a path under the owner's home directory
  or the lab mount. It also rejects tracked per-machine agent state
  (`.serena/`, `.claude/`, any `.env`). It reads every path in the index,
  including binary files and symlink values, and it fails closed if git fails
  or a file goes unread. A line whose meaning *is* the value can carry an
  inline `public-release-ok: <reason>` waiver. `make test-public-releasable`
  covers the gate's rejecting paths. The fleet's values are not in the
  tracked tree: the gate loads them at run time from an untracked pattern
  file (`PUBLIC_RELEASE_PATTERNS_FILE`, the handbook checkout beside the
  repository, or `$XDG_CONFIG_HOME/go-qrdb/public-release-patterns`) and
  fails closed without one. CI reads the `PUBLIC_RELEASE_PATTERNS` secret.
- **A Release workflow is the only path that creates release tags.**
  `.github/workflows/release.yml` is `workflow_dispatch` only. It reads
  `VERSION` from `main`, which is authoritative, and runs
  `make release-alpha` as a dedicated release bot. That creates signed tags
  for the root module, `sdk/go` and `sdk/go/gorm/conformance`, and publishes
  them in one atomic push. It then fetches the remote's tags and runs the
  lockstep audit over them. **BREAKING for anyone cutting releases by hand:**
  `release-alpha` no longer writes VERSION or commits. It tags the merged
  VERSION/CHANGELOG commit and refuses a `VERSION=` argument that VERSION on
  `HEAD` does not name ("merge the VERSION/CHANGELOG bump first"), an unsigned
  `HEAD`, and a `HEAD` the remote's `main` does not hold. It pushes only the
  tags. The bot token is never persisted in the checkout
  (`persist-credentials: false`); only the push step receives it, as a
  per-step header (`scripts/git-extraheader.sh`). `docs/release/` covers
  the flow and the owner's one-time setup (tag protection for `v*`,
  `sdk/go/v*` and `sdk/go/gorm/conformance/v*`, plus the
  `RELEASE_BOT_TOKEN` / `RELEASE_BOT_SIGNING_KEY` secrets and
  `RELEASE_BOT_NAME` / `RELEASE_BOT_EMAIL` variables).
- **SQL row locking and pessimistic concurrency** (docs/row-locking).
  `SELECT ... FOR UPDATE`, `FOR SHARE` and `LOCK IN SHARE MODE` (and
  PostgreSQL's `FOR NO KEY UPDATE` / `FOR KEY SHARE`) now take real row locks
  in every mode, with `NOWAIT`; go-mysql-server discarded the clause. A
  database's new `sql.concurrency` (`optimistic`, the default, or
  `pessimistic`) and the session override `SET qrdb_concurrency` make DML in
  pessimistic mode lock the rows it touches and read them at their latest
  committed version, so concurrent writers of a row wait instead of failing at
  `COMMIT` with 1213. The commit-time compare-and-swap stays in both modes.
  - The lock table (`pkg/rowlock`) is lease-bound records in the database's own
    store over the existing transaction, lease and watch operations -- no new
    Raft command. Waiters queue FIFO by create revision; a crashed holder's
    locks expire with its lease; a leader change loses nothing.
  - Lock-wait timeout per session: `innodb_lock_wait_timeout` (now accepting
    MySQL's 1..1073741824 seconds, default 50; go-mysql-server allowed only 1;
    session scope only -- `SET GLOBAL` is refused, since the engine's globals
    are shared by every tenant on the node) and PostgreSQL `lock_timeout`, failing the statement with MySQL 1205 /
    SQLSTATE 55P03. `NOWAIT` is 3572 / 55P03.
  - A pessimistic `UPDATE` or `DELETE` never writes a row that changed while it
    waited: a row it did not read under its lock (a cascade's child rows) is
    compared with what it matched once the lock is granted, and a mismatch or
    a vanished row fails the statement with 1213.
  - Deadlocks are detected at enqueue; the victim's whole transaction is rolled
    back with 1213 / 40P01, and its later statements fail (25P02 on PostgreSQL)
    until the client ends it. `SKIP LOCKED` is refused (1235 / 0A000).
  - Lock errors are classified on every transport through `pkg/api/errclass`
    (Aborted; SKIP LOCKED Unimplemented).
  - Metrics: `qrdb_sql_row_lock_requests_total{mode,outcome}`,
    `qrdb_sql_row_lock_wait_seconds{mode}`, `qrdb_sql_row_lock_waiters`.
- **Per-database SQL settings** on all six transports, the CLI and YAML:
  `UpdateDatabaseSQL` (`PATCH /v1/databases/{name}/sql`, gRPC/Unix
  `DatabaseUpdateSQL`, QUIC `db.UpdateSQL`, MCP `qrdb_database_update_sql`,
  embedded `UpdateDatabaseSQL`), an `sql` field on create and on database info,
  `qrdb database update-sql --concurrency` and `qrdb database create
  --sql-concurrency`, and `databases[].sql.concurrency` in the configuration
  file. The in-process driver takes a `concurrency=` DSN parameter.
- `sealedwindow.Admission`, `Names.Transition` and the per-transport
  `*Transition` derivations; `ListenerProbe.Admit`.

### Changed

- **BREAKING (Go API):** `middleware.SOMiddleware` takes a
  `role.NameResolver` as its second parameter. `rest.Server` passes its
  role service (`rest.WithRoleService`); a server with the gate enabled and
  no role service refuses SO-gated routes with `503`.
- `auth.Identity.HasRole` documents that it compares role identifiers, and
  its parameter is renamed `roleID`.
- REST `POST /v1/authfactors/{id}/verify` reads the body's `factor_name`
  (which the SDK REST client already sent) and passes it to the service, like
  the other five transports, so a mismatch is refused rather than ignored.
- **BREAKING (gRPC API and generated Go client):** `BarrierRecoveryDelete`
  takes `BarrierRecoveryDeleteRequest{so_pin}` instead of
  `google.protobuf.Empty`, so QUIC can carry the SO PIN for it. The wire is
  compatible (an empty request encodes exactly as `Empty` did); Go code
  calling or implementing `QRDBServiceClient`/`QRDBServiceServer.BarrierRecoveryDelete`
  must pass or accept `*qrdbv1.BarrierRecoveryDeleteRequest`. The SDK's
  `BarrierDeleteRecoveryKeys(ctx)` signature is unchanged.
- **Breaking: one marker for "this store cannot write atomically".**
  `kvstore.ErrAtomicWriteUnsupported` (`sdk.ErrKVAtomicWriteUnsupported`) is
  the only one. `dao.ErrAtomicWriteUnsupported`, `database.ErrAtomicWriteUnsupported`,
  `custodian.ErrAtomicWriteUnsupported` and `invite.ErrAtomicWriteUnsupported`
  are removed: the DAO refuses with the typed `dao.AtomicWriteUnsupportedError`,
  and the database backends, the service layer, the custodian service and the
  invitation store return the kvstore marker itself.
  `sdk.ErrInviteAtomicWriteUnsupported` is now an alias of
  `sdk.ErrKVAtomicWriteUnsupported` (same value; its message is the kvstore
  one). The marker, and the DAO's typed refusal, also match
  `kvstore.ErrNotApplied` (new SDK alias `sdk.ErrKVNotApplied`): a capability
  refusal writes nothing, so a caller need not reconcile. Before, a caller
  testing one marker missed a refusal carrying another; the server's own KV
  adapter, for one, refused with the database sentinel, which neither the
  kvstore nor the dao marker matched.
- **Breaking: a permitted-method policy names methods this node can
  authenticate with, or it is refused.** The vocabulary is the built-in `mtls`,
  `piv` and `webauthn` plus the `Name()` of every authenticator in the node's
  own chain, including embedder-supplied ones (`ExtraAuthenticators`, the SDK
  node's `WithAuthenticators`). It is per node, never process-wide: the auth
  middleware exposes it (`AuthMiddleware.IsAuthMethod`), and the node's user
  stores receive it through the new `user.WithAuthMethodVocabulary` option
  (`lifecycle.Infra.AuthMethodVocabulary`), late-bound through
  `middleware.AuthMethodVocabulary`. `CreateUser` and `UpdateUser` refuse any
  other token with the typed
  `user.UnknownAuthMethodError` (`sdk.UserUnknownAuthMethodError`), a
  validation error answered 400 / `INVALID_ARGUMENT` on every transport. A
  misspelled token used to be stored and then match nothing, silently refusing
  the method it named. Stored policies are not re-validated: they read back
  unchanged, an update may keep an entry the policy already held (but not add
  a new unknown one), and the authentication gate compares with the new
  `auth.CanonicalAuthMethod` (no vocabulary check), so no existing account is
  locked out. `auth.IsBuiltinAuthMethod`, `auth.BuiltinAuthMethods` and
  `auth.CanonicalAuthMethod` are re-exported by the SDK.
- **`secure.StdinProvider` reads a seekable stream in bulk** (#440). When
  standard input can seek (redirected from a file), the passphrase line is read
  straight into the mlocked buffer in one or two `read(2)` calls instead of one
  per byte, and the bytes read past the newline are sought back, so the rest of
  the stream is still there for its next reader. A pipe or terminal cannot give
  bytes back, so it is still read one byte per call rather than over-read. A
  seek-back the stream refuses fails the read with `*ProviderError`. On
  either path, 100 consecutive empty reads (`(0, nil)`) fail the read with
  `io.ErrNoProgress` instead of spinning forever.
- **The module forge's address is no longer a Makefile default.** Set
  `GITEA_HOST` in the gitignored `.devcontainer/.env` (documented in the new
  `.devcontainer/.env.example`) or in the environment. A `.env` that has a
  token but no host now fails with an error naming the variable. The devcontainer
  compose services resolve modules from the public proxy when `GITEA_HOST` is
  unset.
- **`make gosec`, `gosec-sdk` and `gosec-sarif` scan test variants
  (`-tests`), at `-severity=low`, and skip generated code.** All three take
  their analysis flags from one `GOSEC_FLAGS` variable, so they cannot
  drift. Every site the lower floor surfaced was fixed or annotated with its
  reason. `pkg/index` replaces an `unsafe` float cast with `math.Float64bits`.
  G104 (unchecked errors) is excluded in `_test.go` files only, where every
  site was, so production code stays armed. The REST auth-factor round-trip
  test now verifies the server against its certificate instead of skipping
  verification. The `export_test.go` idiom is gone from
  `pkg/seal` and `sdk/go`, because gosec's loader cannot typecheck an external
  test package that relies on it. Those tests now run in-package.
- **The coverage baseline records the build tags it was measured under, and
  the gate refuses a profile measured under any other set.** Coverage depends
  on which files the tags compile. On the same tree, `pkg/api/grpc`, `quic`
  and `mcp` read 90.7/97.0/91.3% under `BUILD_TAGS` but 77.1/88.6/80.7% under
  `codec_json raft` alone, which lanes had misread as regressions.
  `coverratchet` takes `-tags`, and `make print-build-tags` prints the set.
  The baseline was regenerated under `BUILD_TAGS`. No number was lowered;
  seven packages that measured fractionally under their baseline, within the
  tolerance, keep their previous number.
- **`pkg/server` is at 91.5%, above the 91% floor** (baseline 90.71). New
  tests cover the raft observer promoter, `StartedAsObserver`, and
  `CloseAndPurge`. `CloseAndPurge` is the demotion teardown that deletes a
  demoted replica's ciphertext, and it had no test.
- **`pkg/ratelimit` measures the same coverage on every run.** Its coverage
  used to alternate between 95.60% and 96.70% depending on thread timing,
  which could fail the coverage gate at random. The token-bucket decision is
  now a pure function (`take`), and `Allow`'s compare-and-swap loop only
  retries it. Tests trigger the reaper with explicit ticks instead of sleeping
  on the wall clock. Five consecutive runs produced identical profiles at
  97.75%; only two unreachable panic guards are uncovered.
- **BREAKING: `tenant.ResidueReconcileErrors` is renamed
  `tenant.ResidueReconcileFailuresError`**, to follow the `XxxError` naming
  every other error type here uses (errname). Its fields, message and
  `Unwrap() []error` are unchanged. `errclass.Conditions`, `errclass.Condition`
  and `errclass.RuleCount` move out of a test-only `export_test.go` into the
  package proper, so gosec's `-tests` loader can typecheck the transport
  parity test that reads them.
- **Documentation: approved multi-model database design.** `docs/databases/`,
  `docs/vectors/` and `docs/time-series/` now carry the approved design in which
  every database serves every engine (KV, MVCC and transactions, SQL,
  collections, secondary indexes, vector, time series, leases and watches) on the
  storage backend it chose, for every tenant isolation mode, through one
  per-database storage binding. They also cover removing the database `engine`
  field and the node-wide backend overrides, migrating existing data, a phased
  implementation plan, and the owner's rulings on its sixteen design decisions
  (every recommendation accepted on 2026-09-30; the drop fence and every new Raft
  command ship behind a cluster feature gate). The same change
  corrects claims those folders made about shipped behaviour: a database's
  `backend` does not decide where most databases' data lives; `objstore` is
  refused when a database store is opened; vector data lives in one node-local
  store under the node key, and its `shard_id` selects nothing; similarity search
  returns nothing on namespaces loaded at startup until the index is rebuilt;
  time-series retention policies are stored but never applied; and no check
  refuses etcd on a non-raft database. No code changes.
- **BREAKING (gRPC API and generated Go client):** `BarrierSeal` takes
  `BarrierSealRequest{so_pin}` instead of `google.protobuf.Empty`, so gRPC and
  the Unix socket can carry the barrier SO PIN. The wire is compatible (an empty
  request encodes exactly as `Empty` did). Go code calling or implementing
  `QRDBServiceClient`/`QRDBServiceServer.BarrierSeal` must pass or accept
  `*qrdbv1.BarrierSealRequest`. The SDK's `BarrierSeal(ctx)` signature is
  unchanged, and every SDK transport fills `so_pin` from `sdk.WithSOPIN`.

### Removed

- The tracked `.serena/` project state, the `.claude/skills` directory and the
  `tmp-probe` developer diagnostic. The first two were already in
  `.gitignore`, and the new gate keeps them out.

### Fixed

- **A standalone `sdk/go/node` node serves its named databases.** Its server
  was composed with neither the database catalog nor a database service
  registry, so no database other than the node's own resolved: a
  database-scoped client was refused outright, and databases created at
  runtime were not reopened at the next boot. The node now opens its catalog
  itself (reopening every database a previous run created) and composes the
  server with it and with a registry whose default entry is the node's own
  service graph (`lifecycle.SystemDatabase`), as `qrdbd` does; the clustered
  node's server gains the same wiring. A scoped embedded client serves exactly
  the families its unscoped transport serves, so on a standalone node MVCC,
  leases and watches stay refused.
- **Unix SDK: a successful `VerifyFactor` or `RevokeFactor` no longer returns
  an error.** `mapGRPCError` turned a nil gRPC error into a `RequestError`, so
  both calls reported failure on success.
- The admin account first boot creates in the system tenant is now the System user, so
  `DeleteUser` refuses it. A cluster whose admin predates this is promoted on its next start,
  and at most one System user can exist in a cluster.
- **`SaveIfAbsent` reports a store's call-time atomic refusal as its typed
  refusal.** A store that implements `AtomicKVStore` but refused the write came
  back as a bare store error. It is now `*dao.AtomicWriteUnsupportedError`,
  the same as a store without the method, with the store's refusal kept in the
  chain. Any other store error is still returned unchanged.
- **`sdk/go/driver`: the embedded `Rows.Next` no longer panics on a row
  wider than its columns.** It wrote `dest[i]` for every cell the engine
  returned. `database/sql` sizes `dest` from `Columns()`, so a wider row
  indexed past it inside the caller's `rows.Next`. A row of any other width
  now returns `*driver.RowWidthError`. A narrower row used to leave the
  previous row's values in the slots it did not fill, and it now gets the same
  error. gosec's G602 found this once the severity floor was lowered.
- **A reset no longer lets a session emit AUTO_INCREMENT values from a block
  reserved before it** (#547). After `ALTER TABLE ... AUTO_INCREMENT = 0` or
  a MySQL `TRUNCATE TABLE`, a session still holding part of a pre-reset block
  kept handing those values out while the restarted counter handed them out
  again, so concurrent inserts collided with MySQL error 1062. The block
  allocator now checks a block's epoch once per statement that draws from a
  block it did not reserve, and refills from the post-reset counter when a
  reset has intervened, on this node or any other. A statement costs at most
  one extra linearizable read, however many rows it inserts.
- **PostgreSQL `TRUNCATE TABLE` keeps the table's sequence on every server
  construction path** (#547). A PostgreSQL server composed with
  `WithServiceRegistry` but no database manager built its provider without
  the PostgreSQL protocol, so its relational tables took MySQL's `TRUNCATE`
  semantics and restarted `AUTO_INCREMENT` at 1, and unqualified
  `pg_catalog` relations did not resolve. `pgsql.Server.Start` now
  applies `WithProtocol(ProtocolPostgreSQL)` on both paths. MySQL `TRUNCATE
  TABLE` resets `AUTO_INCREMENT` to 1, matching MySQL.
- **`SET qrdb_isolation` over the PostgreSQL wire took no effect.** Every `SET`
  was a no-op there; `SET [SESSION] qrdb_*` now reaches the engine and
  `lock_timeout` is translated.
- A `COMMIT` that loses its compare-and-swap now ends the session's
  transaction, as InnoDB's rollback on ER_LOCK_DEADLOCK does, instead of
  leaving go-mysql-server in a transaction that no longer exists.
- **A `UNIQUE` index was not enforced by an `UPDATE` inside a transaction that
  left the row's primary key unchanged.** Two rows of one transaction could be
  given the same value and both commit; a value a committed row held failed
  only at `COMMIT`. Such an `UPDATE` now fails with 1062 at the statement, as it
  does in autocommit.
- **A statement that failed part way inside a transaction kept its partial
  effects.** `INSERT ... VALUES (1), (2), (5)` failing on a duplicate `5` left
  rows 1 and 2 to be committed; a foreign-key cascade that timed out on a
  locked child row left its parent deleted. A failed DML statement is now undone
  whole — across every table it edited — while the transaction's earlier
  statements are kept, as in InnoDB. Outside a transaction a multi-row statement
  is still not atomic: each row is its own commit.
- **Auth-factor names have one normalisation.** `authfactors.NormalizeName`
  (case-insensitive) keys factors in `FactorRegistry` (Register, Lookup,
  Unregister) and in `NewAuthFactorService`, which also uses it for dispatch.
  Two factors whose names normalise alike, such as `PIV` and `piv`, are now
  refused with `*authfactors.FactorAlreadyRegisteredError`; before, the second
  silently replaced the first. A nil factor is refused with
  `*authfactors.FactorRegisterError`. `FactorRegistry.List` reports normalised
  names.
- **A user update that raced another update of an index handle failed with a
  bogus conflict.** Re-claiming a username, email or certificate thumbprint
  could find the key held and then, before reading who held it, see the
  holder release it; the update reported the handle as taken (for a
  thumbprint, "already bound to user 0") instead of taking the now-free key.
  The claim is now retried, and a key that changes hands on every attempt
  fails with `user.HandleContendedError`. A rollback that finds its released
  handle already re-inserted for the same user no longer reports it as
  `HandleReclaimedError`. Together these turned a lost concurrent withdrawal
  of the bootstrap admin's keys into an `IndexCompensationError` rather than
  the expected lockout refusal.
- **`qrdbdharness` handed the same port to concurrent test processes.** The
  port reservation table was per process and every process walked the same
  window from the same base, so two `go test` package processes each
  bind-verified a port the other had just released and both used it: a
  listener failed with `bind: address already in use`, or two in-test Raft
  clusters dialled each other and never elected a leader. Every reserved port
  is now also locked across processes by an abstract Unix socket held for the
  life of the process, so callers of `ReserveTCPPorts`/`ReserveUDPPorts` need
  no change. Every reserved port is also verified free on both TCP and UDP,
  so a REST admin port -- whose HTTP/3 sibling binds the same number on UDP
  -- can be taken from either function.

### Security

- **A sealed database now fails closed on every engine and every transport
  (critical).** Sealing a per-database barrier set the database's status to
  `sealed`, but no request path read that status: a database served from the
  shared system shard, a volume tenant's host or a raft group stayed readable
  and writable while reported sealed. The service registry every transport
  resolves through now refuses every accessor for a sealed database the caller
  owns with `service.BarrierSealedError`, which gains a `Database` field and
  maps to `503` / `UNAVAILABLE`. Another tenant naming the database still sees
  not-found.
- `database.SharedStoreBarrierUnsupportedError` was answered as an internal
  error (`500` / `INTERNAL`) on every transport, contrary to the documented
  `FAILED_PRECONDITION`; it and the new `BarrierOffDataPathError` now map to
  Unprocessable -- `422` on REST, `FAILED_PRECONDITION` on gRPC, Unix, QUIC
  and MCP -- because the caller cannot change a database's backend or
  data_dir (the documentation said `400`).
- **The per-database etcd listener is confined to its database and
  authenticates every request (high).** It was wired to the node's raw,
  un-namespaced system-shard services, so any etcd client reaching the port
  read and wrote every tenant's keys, and its only "authentication" was the
  stub `Auth` service. The listener now resolves its MVCC, transaction, watch
  and lease services through the per-(tenant, database) service registry for
  the database's owning tenant, authenticates every RPC with the node's
  authenticator chain, refuses callers from other tenants with a generic
  `etcd: permission denied` that names neither the database nor its owner
  (the detail is logged server-side), checks a
  default-deny RPC-to-permission map, and re-resolves the database on every
  RPC so a sealed or dropped database is refused. Leases are confined to the
  (tenant, database) that granted them on every transport, etcd included: lease
  IDs are global, and the lease view used to list, keep alive and revoke any
  tenant's lease by ID; ownership is now recorded at grant and a foreign ID is
  answered exactly as a missing one (not-found; `Revoke` is the idempotent
  no-op success and revokes nothing; a write attaching it is refused and
  writes nothing). It refuses to start without
  TLS and authentication (`etcd.AuthRequiredError`) or without a catalog and
  registry to confine it (`etcd.ScopeUnavailableError`). An open `Watch` or
  `LeaseKeepAlive` stream ends with the database's typed refusal the moment
  the database is sealed or dropped, as every transport's watch stream does.
- **SQL `DROP DATABASE` can no longer drop another tenant's database, and SQL
  `CREATE DATABASE` is owned, metered and authorized (critical).** Over the
  MySQL and PostgreSQL wire, `DROP DATABASE` called the database manager by
  name with no ownership check, and the privilege plane deferred the statement
  to an RBAC check nothing performed, so any tenant could drop any tenant's
  database; `CREATE DATABASE` created a system-owned database outside the
  caller's quota. Both statements now go through the tenant-scoped
  `DatabaseService` (`CreateForTenant` / `DropForTenant`) and check
  `databases:create` / `databases:delete` for the session's tenant. A listener
  without that service or an authorizer refuses both to non-system sessions
  with `provider.DatabaseLifecycleUnavailableError`. New options:
  `provider.WithDatabaseService`, `provider.WithLifecycleAuthorizer`,
  `mysql.WithDatabaseService`, `pgsql.WithDatabaseService`.
- **RAG chunk text and document records are confined to the caller's tenant
  and database (medium).** They were keyed `rag:{namespace}:{document}:{chunk}`
  in the shared store with no tenant or database, so two tenants using the same
  namespace and document ID overwrote each other's chunk text, a retrieval
  returned the other tenant's text for its own vector hits, and listing and
  deleting reached across tenants; RAG also ignored database seal, ownership,
  quota and isolation, and its vectors were scoped by tenant only, so a
  document in one database overwrote another database's vector. Chunk text,
  document records and vectors are now all resolved through the node's
  per-(tenant, database) service registry on every call
  (`service.RAGBinding`, bound by the server at start; `service.ScopedRAGStore`
  and `service.NewScopedVectorStoreAdapter`), so a sealed database refuses
  RAG, an unknown or foreign `X-QRDB-Database` is a not-found, and vectors are
  per database. On a node with request authentication a RAG call that carries
  no tenant is refused (`service.RAGTenantRequiredError`) instead of acting
  for the system tenant. REST and gRPC/Unix now also bind the request's database as the
  canonical `tenant.DatabaseFromContext` scope, as QUIC and MCP already did.
- **Another tenant's database is answered as missing (medium).** Database
  names are one node-wide namespace, and a tenant addressing another tenant's
  database got an access denial (`403` / `PERMISSION_DENIED`) where a missing
  name got not-found, so it could probe which names other tenants hold; SQL
  `HasDatabase` and `USE` also answered for every tenant's databases. Every
  tenant-scoped database operation (get, drop, update, seal, unseal, barrier
  status) now returns `database.NotFoundError` for another tenant's database,
  and the SQL provider answers only for the session tenant's databases -- and
  for the configured default SQL database (`sql.default_database`), which
  every tenant has in its own namespace, exactly as the service registry
  resolves it. The
  ad-hoc SQL query (`/sql/query`, `SQLQuery`) resolves the database through
  the service registry before the node-wide catalog, so an unused name and
  another tenant's name get the same not-found. The
  catalog re-key that scopes names per tenant — and closes the last signal, a
  create colliding with another tenant's name — is separate work.
- **Database names that collide with node state are reserved (medium).** A
  volume-isolated tenant's database lives at `{DataRoot}/databases/<name>`,
  beside the tenant's own Raft host, so a database named `wal` or `nodehost`
  was created inside that host's WAL or node-host directory. Both names (any
  case) are now refused for every database with `database.ReservedNameError`
  before any directory is made. Database-catalog refusals also map to their
  documented status on gRPC, Unix, QUIC and MCP, where they were answered as
  internal errors: not found is `NOT_FOUND`, a taken name `ALREADY_EXISTS`, an
  invalid or reserved name `INVALID_ARGUMENT`.
- **A database-scoped client is never handed the default database.** Over the
  embedded transport `Client.DatabaseScoped(name)` returned the unscoped
  client, so a caller that named database X read and wrote the node's default
  database. The embedded transport now implements `transport.DatabaseScoper`
  through the node's per-(tenant, database) service registry
  (`embedded.WithServiceRegistry`, wired by `sdk/go/node`), resolving each
  data-plane call -- KV, MVCC, transactions, batches, watches, leases, indexes,
  time series, vectors, collections, locks, RAG -- as the network transports'
  server does, so a sealed or dropped database is refused; without a registry,
  and over any transport that cannot scope, `DatabaseScoped` refuses with
  `transport.DatabaseScopeUnsupportedError`. Embedded SQL queries resolve
  through the same registry: they used to read the node's default store under
  the database's prefix, which served a sealed database's data and, on a scoped
  transport, prefixed the already-scoped store a second time.
- **A node reports, and warns at startup, when seal is enabled but its data
  is stored unencrypted (high, report only).** The barrier's encrypted
  filesystem covers a standalone node's `pebble` backend, not its `file` or
  `objstore` backend, which kept data in plaintext with nothing saying so (a
  raft node refuses at start any state machine storage that would persist
  plaintext, so it reports no gap). Every
  composition root now logs one WARNING per gap at boot, and the capability
  report carries them as `UnencryptedAtRest` (`unencrypted_at_rest` field 20
  in `CapabilitiesResponse`) on REST, gRPC, Unix, QUIC, MCP and embedded, and
  in `qrdb admin capabilities`. Encrypting those stores is separate work.
- **`databases:create_raft` and `databases:create_volume` are enforced
  (high).** Both were documented as required in addition to `databases:create`
  and checked nowhere, so any holder of `databases:create` could provision a
  dedicated Raft group (`backend: raft`, or any database of a raft-isolated
  tenant) or a database on a volume-isolated tenant's volume. Every create
  path -- REST, gRPC, Unix, QUIC, MCP and SQL `CREATE DATABASE` -- now checks
  them against the caller's tenant through the database service the server
  composes (`service.NewProvisioningAuthorizedDatabaseService`); a refusal is
  `service.ProvisioningDeniedError`, `403` / `PERMISSION_DENIED`.
- **The boot-time sealed-window listener served anonymous requests against an
  unsealed barrier (high).** That listener has no authentication middleware,
  because while the barrier is sealed there is no identity plane. Its gate
  became a pass-through the moment the barrier unsealed, and the listener kept
  serving until the next 250 ms poll noticed and `Stop` completed. In that
  window every route registered on it ran anonymously against an unsealed
  barrier, including `DELETE /v1/barrier/recovery` (proven by
  `TestServeSealedWindow_RefusesAnonymousRequestsAfterUnseal`, which destroyed
  the recovery keys with an anonymous request on the unfixed tree) and the
  credential routes. The gate now samples the barrier on every request and
  refuses everything once it is unsealed, on REST, gRPC, Unix, QUIC and MCP.
- **Anonymous sealed-window verbs are limited to liveness, credential
  possession, first-boot trust on first use, and handler-bound handshakes.**
  The officer list disclosed officer enrollment anonymously. Recovery-key
  delete, rekey and external officer enrollment were destructive or
  SO-only verbs that were allowlisted, and only the service's own refusal
  while sealed kept the first two inert.
- **A forwarded on-behalf request no longer lets any cluster-CA certificate
  make the leader buffer an unbounded body (medium).** The REST middleware
  read the whole body of a request carrying on-behalf headers, with no bound,
  to compute the digest the assertion binds. It did this before verifying the
  assertion, and for any certificate chaining to the cluster CA, including a
  user's. It now refuses a presenter that is not a cluster node
  (`forward.ClusterPeer`) before reading anything, and reads a cluster node's
  body under `forward.MaxBodyBytes`: 16 MiB + 64 KiB, the largest body a REST
  handler forwards.
- **MCP authenticates every request before the MCP layer reads it, and bounds
  the body (medium).** With authentication configured, a request that no
  authenticator accepted went on to mcp-go without an identity, and only tool
  calls were refused. `initialize`, `ping` and `tools/list` were answered to
  anonymous callers, and `tools/list` enumerates the whole admin tool surface.
  mcp-go also parsed bodies of any size. Every request to `/mcp` is now
  authenticated at the HTTP layer and refused with `401` if it fails, with
  `WWW-Authenticate` when a bearer challenge is configured. An authenticated
  caller's body is bounded at the batch-import limit plus 1 MiB of envelope.
  On a listener without authentication (the boot-time sealed-window
  listener, or a node with authentication off), every caller is anonymous,
  and the bound is 1 MiB.
- **gRPC server reflection is no longer exempt from authentication (low).**
  The gRPC and Unix interceptors let `/grpc.reflection.*` through with no
  credentials. qrdb registers no reflection service, but a composer that
  registered one exposed the node's whole schema anonymously. Reflection is
  now authenticated and default-denied like any other unmapped method. A
  composer that wants it anonymous declares it through
  `GRPCPublicMethodLister`.
- **ceremony-sign-csr compares the SO PIN in constant time and is throttled
  (medium).** The endpoint is reachable without authentication on the
  boot-time sealed-window listener, and the SO PIN is its only authorization.
  It compared the PIN with `!=` and answered every guess immediately, which
  made it a timing side channel and an unthrottled online PIN oracle. The
  comparison is now `subtle.ConstantTimeCompare` over SHA-256 digests.
  Checks run one at a time, and each failure is counted before the next
  check. After 5 failures in a row, every further failure locks the check
  for a backoff that doubles from 1 s up to 15 min. A correct PIN resets the
  count. Only failed checks count, so anonymous load cannot starve a
  legitimate officer. While locked, the call fails with
  `service.SignCSRThrottledError`, which unwraps to `pin.ErrPINLocked` and is
  answered `429` / `RESOURCE_EXHAUSTED` on every transport. The REST 429
  carries `Retry-After` with the wait in seconds, rounded up. The SO PIN needs
  at least 40 bits of entropy (docs/ceremony).
- **Neither the native QUIC listener nor REST's HTTP/3 listener accepts 0-RTT
  any more (medium).** The native listener set `Allow0RTT`. The HTTP/3
  listener had no QUIC configuration, and quic-go's `http3.Server` allows 0-RTT
  by default. On both, a resumed client's early data was accepted before the
  handshake completed. 0-RTT data is replayable, so an on-path attacker could
  resend a captured request, including a state-changing one, and have it
  processed again. A resumed client now completes a full handshake first. The
  HTTP/3 listener also takes the native listener's idle, keep-alive and
  stream limits.
- **Anonymous officer enrollment closes once the ceremony is over, including
  after a restart while sealed (high).** Officer and recovery-officer
  registration CA-sign any CSR they are given, and they are anonymous on the
  boot-time sealed-window listener. They were flagged `PreOperationalOnly`,
  but nothing enforced that. Ceremony state lives in memory, so a daemon
  restarted while sealed started a fresh, pre-operational ceremony and gave an
  anonymous caller a certificate signed by the cluster CA. The listener now
  refuses both verbs once enrollment has closed. It closes when the ceremony
  is operational, when the barrier records an operational ceremony (a marker
  written to the barrier's plaintext store, readable while sealed), or when
  the barrier is initialized. It fails closed. The refusal is `403`
  `ENROLLMENT_CLOSED` / `PERMISSION_DENIED`. The ceremony enforces the same
  rule itself, under its own lock: `RegisterOfficer` and
  `RegisterRecoveryOfficer` refuse an anonymous request with
  `ceremony.ErrEnrollmentClosed` once enrollment has closed.
- **The anonymous officer claim status no longer names the officers
  (medium).** `officer-claim-status` is anonymous on the boot-time
  sealed-window listener, so the first-boot wizard can follow claims, and it
  returned every officer's username, certificate thumbprint and auth factor
  to any caller. That listener now answers with the share index, enrolment
  and claim times and pending state only. `BarrierOfficerClaimStatus` redacts
  by default, for every caller whose context carries no authenticated
  identity and for every request the sealed-window listener marks anonymous
  (`sealedwindow.WithAnonymous`). An authenticated caller on a regular
  listener still gets the full record. The wizard keys its claim table by share index and names
  rows from its own registrations.
- **The SO dual-factor gate recognises a real Security Officer, and only
  one.** `middleware.SOMiddleware` checked `identity.HasRole("so")`, but an
  authenticated identity's `Roles` holds role *identifiers* (from the user
  record's `RoleIDs`). Every real officer was refused with
  `403 SO role required`, and an identity whose roles carried the literal
  string `so` (a token claim for a subject with no user record) passed the
  role step; the SO PIN was still required. The gate now resolves the SO
  role through the role store with the new `role.HoldsSystemRole`: the
  caller must carry the identifier under which its own tenant's `so` role is
  persisted. Names never match. A role store that cannot answer refuses with
  `503`.
- **The SO dual-factor gate now guards every network transport, not only
  REST.** With `auth.so_dual_factor` on, the SO-gated operations
  (`middleware.SOPermissions`: barrier rekey, Shamir initialize, recovery
  generate, root-key rotation, ceremony initialize, custodian enrol and revoke,
  CSR signing) were reachable over gRPC, the Unix socket, QUIC and MCP by any
  caller RBAC admitted, such as a token principal granted `seal:rekey`, with
  no SO role and no SO PIN. The gate is now one implementation,
  `middleware.SOGate`, which the orchestrator installs on all of them
  (`WithSOGate` on each server). The PIN travels in `x-qrdb-so-pin` metadata
  on gRPC and Unix, in the `X-QRDB-SO-PIN` header on MCP, and in the request's
  `so_pin` field on QUIC. The Go SDK sends it on every network transport
  from `sdk.WithSOPIN` (see Added). Refusals are typed
  (`SOFactorError`, `SORoleRequiredError`, `SORoleUnresolvedError`) and map to
  the same class on every transport.
- **`VerifyFactor` is dispatched to the enrollment's own factor.** The
  caller's factor name used to choose the factor that verified a challenge,
  and the enrollment was consulted only when the name was empty. With two
  factors registered, a caller could answer a strong factor's challenge
  through a weaker one: reproduced on gRPC, Unix, QUIC, MCP and embedded with
  a junk response. The service now loads the enrollment, dispatches to its
  factor, and refuses a non-empty caller name that differs with the typed
  `*authfactors.FactorMismatchError` (`AuthFactorMismatchError` in `sdk/go`):
  400 on REST, `INVALID_ARGUMENT` on gRPC and Unix, `StatusInvalidArgument` on
  QUIC, `CodeInvalidArgument` on MCP. The challenge is consumed either way. A
  challenge whose enrollment was revoked after it was issued is now refused
  too (`*authfactors.EnrollmentRevokedError`). Factor names are compared
  through the registry's own normalisation (case-insensitive), which the
  registry now also applies when a factor is registered, so a factor whose
  `Name()` is not lower case can be enrolled at all.
- **A client certificate whose SPIFFE SANs name more than one tenant, or that
  carries any SPIFFE SAN outside the pinned trust domain, is refused.** The
  certificate-to-tenant rule read only the first tenant SAN, so the issuer's
  SAN order chose the tenant, and a foreign-domain SAN after a good one was
  ignored. Every URI SAN is now examined, on login and in
  `sdk.TenantFromClientCert` alike. The refusal is the new
  `*auth.CertificateTenantError` (`sdk.ClientCertificateTenantError`), which
  still matches `ErrInvalidCertificate`, so every transport answers as
  before; an ambiguous certificate carries the new
  `*spiffe.AmbiguousTenantError` (`sdk.SpiffeAmbiguousTenantError`).
- **`qrdbd start` makes its memory unreadable to other processes before it
  holds any key** (#554). mlock kept protected memory out of swap, but a
  process running as the same user could still read every enclave through
  ptrace, `/proc/<pid>/mem` or a core file. `start` now calls the new
  `secure.HardenProcess` first: `prctl(PR_SET_DUMPABLE, 0)` on Linux,
  `ptrace(PT_DENY_ATTACH)` on macOS, and a logged no-op where no mechanism
  exists (Windows). It is unconditional; `QRDB_SECURE_ALLOW_PROCESS_INSPECTION=true`
  leaves the process inspectable for a debugging session and logs a warning.
  A malformed opt-out value, or a platform refusal, fails `start` with
  `*ProcessInspectionOptOutError` / `*ProcessHardeningError`. Re-exported from
  `sdk/go/secure` (with `AllowProcessInspectionEnv` and both error types) so
  embedding binaries apply the same posture at their own startup. Operators:
  `gdb -p`/`dlv attach` now need the opt-out, a crash writes no core file, and
  non-root monitoring agents lose `qrdbd`'s `/proc/<pid>/` entries.
  Hardening is the first statement of the start command, before any
  configuration is read. A tracer already attached (`TracerPid` in
  `/proc/self/status` not 0) fails start with `*ProcessTracedError`. In FIPS
  140-3 mode the opt-out is refused (`*ProcessInspectionOptOutRefusedError`),
  and an honoured opt-out is written to the compliance audit trail
  (`disable-inspection-hardening`); a node that cannot record it does not run.
  `HardenProcess` returns the outcome (`ProcessHardened`,
  `ProcessInspectionAllowed`, `ProcessHardeningUnavailable`) with the error.
- **The `qrdb` desktop and CLI binary hardens itself the same way** (#554). It
  unseals barriers and reads passphrases, PINs and shares too, so it calls
  `secure.HardenProcess` before the GUI/CLI branch, honours the same
  `QRDB_SECURE_ALLOW_PROCESS_INSPECTION` opt-out, and exits 1 on a refusal. Its
  warning goes to standard error so it never mixes into a command's output.
- **No anonymous request on the boot-time sealed-window listener runs across
  the unseal (medium).** The listener's gate checked the barrier when a
  request arrived, so a request admitted while sealed could finish after
  another request had unsealed the barrier, running anonymously against an
  unsealed barrier. Requests now hold the listener's `sealedwindow.Admission`
  until they complete. The verbs that can initialize or unseal the barrier
  (`sealedwindow.Names.Transition`) hold it exclusively, so they wait for
  the requests in flight, and every later request is refused.
- **A tenant's Security Officer can no longer operate the node barrier.**
  `seal:rekey`, `seal:rotate-root-key`, `seal:generate-recovery` and
  `seal:initialize-shamir` were ScopeSelf. For an operation that names no
  tenant, ScopeSelf compares the caller's tenant with itself, so the check was
  vacuous: an SO of tenant A, presenting tenant A's own valid SO PIN, could
  rekey or regenerate recovery keys for the node barrier that protects every
  tenant -- on every transport, gate on or off. They are now `ScopeCross`:
  system-tenant identities only.
- **The Go SDK sends the SO PIN only over TLS, and only to the configured
  host.** `sdk.WithSOPIN` attached the PIN on gRPC and Unix connections with
  insecure credentials, on MCP to `http://` URLs, and on REST to every redirect
  hop, including a cross-host or `http` one. gRPC and Unix now send it only over
  TLS credentials. REST and MCP send it only on an `https` hop to the configured
  host, checked on every hop. A PIN-bound call otherwise fails before anything
  is sent, with the new `sdk.SOPINRequiresTLSError` (plaintext) or
  `sdk.SOPINForeignHostError` (a redirect elsewhere).
- **A configured barrier SO PIN is required to seal the node barrier on every
  transport, and only the system tenant may seal it.** REST and MCP verified
  the request's `so_pin` against the PIN backend. gRPC and the Unix socket took
  `google.protobuf.Empty` for `BarrierSeal`, and QUIC ignored the request body,
  so on those three transports the configured PIN was skipped. They now verify
  it the same way. Separately, `seal:seal` was ScopeSelf. For an operation that
  names no tenant, ScopeSelf passes vacuously, so another tenant's Admin or
  Operator could seal the node barrier and stop every tenant's data plane. It
  is now `ScopeCross`, like the other node-barrier verbs.
- **The SO dual-factor gate fails closed without authentication on every
  transport.** The orchestrator installed the gate on gRPC, Unix, QUIC and MCP
  only when an auth middleware was configured, so with `auth.so_dual_factor`
  on and authentication off, those four transports skipped the gate. REST
  already refused in that case. One predicate (`so_dual_factor` set and an SO
  authenticator wired) now governs all five transports. With no one to
  authenticate, the gate refuses every SO-gated operation.
- **The SO PIN is held in protected memory, on the server and in the Go
  SDK.** The server's per-request SO factor (`middleware.SOFactor.PIN`) and
  the SDK's PIN bound by `sdk.WithSOPIN` were plain byte slices, which
  `make secure-analyzer` refuses. Both are now a `secure.Secret`: sealed when
  the PIN is read, opened only for the verifier or the wire write, and
  destroyed when the gate has decided or `release` runs. The server builds
  the factor with the new `middleware.NewSOFactor` (and
  `middleware.SOFactorFromHTTPRequest` for REST and MCP). A PIN the node
  cannot hold in protected memory is refused with the new
  `middleware.SOPINUnavailableError` (503 / `Unavailable`), not reported as a
  bad PIN.

### Known limits

- **A database's seal is enforced per node.** Its barrier and its `sealed`
  status are node-local, not replicated: sealing it on one node of a cluster
  does not seal it on the others, and a node on which it is unsealed keeps
  serving it. Seal it on every node that serves it.
- **A barrier off the data path is only as good as its seal.** A
  barrier-enabled record written before this release refused that shape (a
  raft database's, or a volume- or raft-isolated tenant's) fails closed while
  sealed, and once unsealed its data is served unencrypted from the store the
  barrier does not wrap.
- **A stream's permission is decided when it opens.** A change to the
  caller's roles applies to its next etcd stream or watch, not to one already
  open. (A seal or a drop does end open streams.)
- **Database names are still one node-wide namespace.** Another tenant's
  database is answered as missing everywhere except creation: a create naming
  a database another tenant holds fails with `database.AlreadyExistsError`.

## [0.23.0-alpha] - 2026-09-30

Tracks go-quicraft v0.15.0-alpha. BREAKING: wipe each node's Raft WAL directory and re-sync from peers when upgrading.

### Changed — BREAKING

- **go-quicraft v0.14.1-alpha → v0.15.0-alpha** (and go-enclave v0.6.0-alpha →
  v0.6.1-alpha) in all three modules. go-quicraft 0.15 changes its WAL
  format in place (hard-state record `0x02` → `0x0d`; the remove-node record
  `0x06` is retired): **before upgrading a node, wipe its Raft WAL directory
  and let it re-sync from its peers** (or restore from a snapshot). A replica
  removed with `RemoveData` is retired for good; rejoining needs a new replica
  ID. Snapshot artifacts exported by earlier builds carry no integrity trailer
  and are refused on import; export them again after upgrading. The release
  also hardens snapshot integrity end to end and keeps lease reads safe across
  follower restarts.
- `database.MultiplexLogDB` implements the new `logdb.LogDB.NodeRemoved`,
  dispatching to the WAL that owns the shard.

## [0.22.0-alpha] - 2026-09-30

Merge train 2: transactions and guarded batches on every state machine type, O(1) TRUNCATE, one error-classification table, node lifecycle and go-quicraft 0.14 member identities, tenancy reconcile and atomic tenant create, certificate/auth posture (SPIFFE SAN, mTLS binding policy, single-owner REST routes, attested-caller gate), and collection lifecycle states.

### Added

- **Pre-bound listeners for the embedded server** (#374).
  `server.WithRESTListener`, `server.WithRESTPacketConn` and
  `server.WithGRPCListener` (re-exported as `sdk.WithRESTListener`,
  `sdk.WithRESTPacketConn`, `sdk.WithGRPCListener`) hand the server sockets the
  embedder already bound, so a kernel-assigned port is never released between
  reservation and serving — on TCP or on the HTTP/3 UDP side. A handed-over
  socket outlives any one server, so REST, HTTP/3 and gRPC keep answering on
  the same port after an in-process encryption migration rebuilds the server;
  the SDK node closes the sockets in `Node.Close` and when
  `NewStandaloneNode` / `NewClusteredNode` return an error, and a `Start` that
  refuses (including for a disabled protocol, `*server.PreboundSocketError`)
  closes them. A
  pre-bound HTTP/3 socket is held to the TCP listener's host and port. Where
  pre-binding is impossible — the multi-node Raft transport, which go-quicraft
  binds and advertises itself — port 0 is refused with a
  `*cluster.RaftPortZeroError`, by the SDK node and by `qrdbd start` alike.
  A zero admin or gRPC port on the SDK node still selects the fixed default.
- **Shared-prefix tenants have real per-tenant secondary indexes** (#40,
  priority 1). A DAO over a `StoreResolver`-served store for a `prefix`
  tenant now answers `QueryByIndex` from an engine-maintained index confined to
  that tenant, instead of refusing with `kvstore.ErrIndexUnsupported` and
  planning a full scan. `lifecycle.kvStoreAdapter` hands the engine the
  caller's absolute key prefix made relative to the namespace the index name
  carries, so the maintained scope is exactly the prefix the keys carry for a
  `{tenant}/` store, a `{tenant}/{database}/` namespace and a DAO built
  `WithDatabase` alike; a name outside its keys' namespace is still refused.
  `KVEngine.PutWithIndexes`/`DeleteWithIndexes` select indexes by scope, like
  the Raft apply path, instead of by the key's first path segment. Encrypted
  tenant stores (`barrier`, `volume`, `raft`) still refuse index operations.
  **Behaviour change for direct engine users:** an UNSCOPED index — created
  with neither a namespace in its name nor a declared key prefix — is now
  maintained by `PutWithIndexes`/`DeleteWithIndexes` for every key, exactly
  as the Raft apply path has always maintained it. Before, those two helpers
  updated an index only when its name began with the key's first path segment
  (`{entityType}.`), so an unscoped index missed writes the Raft path indexed.
  Indexes registered through `RegisterEntityType` are scoped to
  `{entityType}/` and are unaffected. To keep an index to one keyspace,
  declare its key prefix.
- **Retired tenant ids are enumerable** (#470). `ListDeletedTenants` returns a
  page of deleted tenant ids with their deletion records, in ascending id
  order — a paged range read over the tombstone prefix, with an opaque page
  token. It is on every transport: REST `GET /v1/tenants/deleted/list`, gRPC
  and Unix `TenantListDeleted`, QUIC `tenant.ListDeleted`, MCP
  `qrdb_tenant_list_deleted`, the embedded client, and
  `qrdb tenant list-deleted`. New cross-scope permission
  `tenants:list_deleted`.
- **Every node reclaims a deleted tenant's directories** (#469). A node that
  was down during a tenant delete, or held the tenant without hosting a shard,
  kept its directories and the record that restored its barrier at the next
  boot. A reconcile pass now runs at boot, before any tenant barrier is
  restored, and on demand as `ReconcileDeletedTenants` on every transport (REST
  `POST /v1/tenants/deleted/reconcile`, gRPC and Unix `TenantReconcileDeleted`,
  QUIC `tenant.ReconcileDeleted`, MCP `qrdb_tenant_reconcile_deleted`, the
  embedded client) and as `qrdb tenant reconcile-deleted`. The tombstone is its
  only predicate, and it never removes a directory a live tenant claims; what
  it leaves is reported with the reason. New cross-scope permission
  `tenants:reconcile_deleted`, and `seal.BarrierRegistry.ReleaseTenant` /
  `DropTenantRecords` / `TenantDataRootCandidates`. The live set it guards
  with is read strictly (an unreadable tenant record aborts the pass) and
  re-read before each removal; a non-canonical recorded path is never removed;
  and the records naming a directory are dropped only after it is removed, so
  a failed removal is finished by the next pass. `DestroyTenant` now removes
  the `DataRoot` before dropping those records, for the same reason, and
  registering a `volume`/`raft` tenant at a `DataRoot` another tenant's record
  on the node still names is refused (`seal.DataRootClaimedError`, surfaced as
  `tenant.DataRootOverlapError`; when the claimant is a retired tenant the
  error says so and its new `Remedy` names `qrdb tenant reconcile-deleted`; a
  failure to read the records to check is `seal.ClaimedRootCheckError`).
  Recorded paths are judged **as recorded**: `TenantDataRootCandidates` returns
  them uncleaned (de-duplicated on the cleaned form), so a dotted record such
  as `/srv/qrdb/x/..` is skipped as `not-canonical` instead of being cleaned
  into its parent and removed. A directory inside a reserved root is refused
  unless it lies strictly inside a volume-pool base, so a pool carved out of
  the data directory stays reclaimable. A `DataRoot` that is a **mount point**
  is reclaimed: the new `seal.BarrierRegistry.RemoveDataRoot` empties the
  directory and treats an emptied root refusing rmdir with `EBUSY`/`EXDEV` as
  reclaimed, instead of failing forever (`DestroyTenant` and the reconcile both
  use it; the filesystem is injectable through `seal.DirectoryRemover` /
  `seal.WithDirectoryRemover`). A failure for one retired id no longer stops
  the pass: per-id failures are collected and returned as
  `tenant.ResidueReconcileErrors` alongside the report, and only a failure to
  read the tombstones or the live set aborts. Every skipped path is logged at
  `WARN`, and an id already fully reclaimed on the node (no directory, and no
  state per the new `seal.BarrierRegistry.HasTenantState`) is passed over
  untouched. On the wire, a partly failed pass answers success with its
  report, whose new `failed` list (`tenant.FailedReclaim`; protobuf
  `TenantResidueFailure`, field 4 of `TenantReconcileDeletedResponse`) names
  each failed id; `tenant.ReconcileResponseError` is the one rule every
  transport applies. The Go SDK returns the response and a
  `transport.ReconcileDeletedTenantsError` together, and
  `qrdb tenant reconcile-deleted` prints the report with a `failed` row per id
  before exiting non-zero.
- **`tenant.DataRootOverlapError` is a caller error on every transport.** A
  create whose `DataRoot` overlaps a volume pool, a reserved data root or
  another tenant's `DataRoot` -- a retired tenant's unreclaimed one included,
  whose message carries the remedy -- was unmapped and reported as 500 /
  `INTERNAL`. It is now 409 on REST and `FAILED_PRECONDITION` on gRPC, the
  Unix socket, QUIC and MCP; the embedded transport returns the typed error.
- **Client certificates carry the SPIFFE tenant SAN when a trust domain is
  configured.** With `auth.spiffe_trust_domain` set, the built-in CA mints
  `spiffe://<trust-domain>/tenant/<tenant>/user/<username>` into every client
  certificate, so the authenticator's trust-domain pin is exercised by the
  deployment's own certificates: they resolve through the pinned SAN, and a node
  pinned to a different domain refuses them. With no trust domain the
  certificate is unchanged and carries no SubjectAltName. A certificate whose
  SAN would not parse back to its Subject's tenant and username is refused with
  `cert.SPIFFEIdentityError` and nothing is minted, and the ceremony CA's
  `SignCSR` refuses a CSR whose tenant SPIFFE SAN disagrees with its OU
  (`ceremony.CSRTenantSANError`). `auth.spiffe_trust_domain` is validated at
  config load with the same round trip. New: `cert.WithTrustDomain`,
  `lifecycle.Infra.SPIFFETrustDomain`, `spiffe.ValidateTrustDomain`,
  `spiffe.TrustDomainError` (carries the rejected value).
- **`auth.mtls_binding_policy: enrolled | required`.** `enrolled` (the default)
  is today's behaviour: an account's certificate bindings are enforced once it
  has one. `required` refuses every certificate login that does not present a
  key enrolled on the account it names — an account with no bindings, and a
  certificate naming no account, get 401 with
  `middleware.CertBindingRequiredError`, which quotes the thumbprint to enrol.
  Because it is a lockout risk, a node under `required` **refuses to boot**
  when the bootstrap admin holds no unexpired binding
  (`firstboot.AdminCertBindingRequiredError`), and `firstboot.Initialize`
  refuses before writing anything when it could not bind the admin it would
  create, and verifies the admin it produced. The SDK node refuses to start with
  an existing unbound admin and runs first boot under its own policy through
  `Node.FirstBoot` (only that path guarantees no lockout on an embedded node).
  Every user store under `required` refuses every change that would leave the
  bootstrap admin unable to log in by certificate -- deleting, renaming,
  disabling or suspending it, dropping mtls from its methods, or leaving it
  with no live binding, whichever verb makes it -- with
  `user.BootstrapAdminLockoutError{Reason}` (409 on REST, FAILED_PRECONDITION
  on gRPC and QUIC, CodeFailedPrecondition on MCP), judged inside the store's
  conditional write. The recovery path for an admin locked out by expiry is to
  switch to `enrolled` and restart. The boot check also refuses an admin whose
  lifecycle forbids login or whose method policy excludes mtls, through the
  same predicate (`user.AdminLoginBlocker`). `AddCertBinding` now refuses an
  expiry that is not in the future, for every account
  (`user.CertBindingExpiredError`: 400 / INVALID_ARGUMENT). New:
  `user.WithLogger`; `lifecycle.UserStoreOptions` takes the store's logger.
  The daemon judges the admin through the linearizable store with a bounded
  wait, so a follower that has not yet replicated the admin's binding does not
  refuse to boot. The admin's deadline is exported as
  `qrdb_auth_admin_cert_binding_not_after_timestamp_seconds`, refreshed on
  every boot check and every committed change to the admin, with a warning
  inside 30 days under `required`. An unrecognised value fails configuration validation and the
  boot. Forwarded on-behalf requests,
  module session tokens and a sealed tenant's IAM window are not gated, as
  documented. New: `auth.MTLSBindingPolicy`, `auth.ParseMTLSBindingPolicy`,
  `middleware.WithMTLSBindingPolicy`, `wiring.AuthMiddlewareConfig.MTLSBindingPolicy`,
  `firstboot.Dependencies.MTLSBindingPolicy`, `firstboot.VerifyAdminCertBinding`,
  `firstboot.VerifyAdminCertBindingWithin`, `node.(*Node).FirstBoot`,
  `bootstrap.SystemConfig.MTLSBindingPolicy`, and their `sdk/go` re-exports.
- **Attested-caller gate.** For the permissions a composer names, a request
  that authenticated and that RBAC granted is admitted only when the injected
  `auth.PeerAttestationVerifier` reports the peer on the connection as
  currently attested: fresh evidence (the gate enforces its own
  `MaxEvidenceAge`), for that peer's certificate, that passed. Every other
  outcome — no evidence, stale or future-dated evidence, evidence for another
  peer, a rejected state, a verifier error, no peer certificate, a forwarded
  (on-behalf) identity — refuses,
  and a refusal is audited as its own `auth.peer_attestation` record with the
  reason and whether it is a security event. One option gates REST, gRPC,
  unix, QUIC and MCP, and the tenant-barrier bootstrap windows are gated too;
  the embedded transport has no peer and is not gated.
  qrdb owns only the seam: the verifier is go-xkms's. New:
  `auth.PeerAttestationVerifier`, `middleware.NewPeerAttestationGate`,
  `middleware.WithPeerAttestationGate`,
  `wiring.AuthMiddlewareConfig.PeerAttestation`,
  `node.WithPeerAttestationGate`, and `sdk/go` re-exports. Docs:
  `docs/attested-caller/`.
- **Collection lifecycle states.** A collection is `creating` until every
  declared index exists, `active` while it serves documents, and `dropping`
  from the moment a drop starts. Every state is reported by info and list; only
  an `active` collection serves document reads and writes
  (`service.CollectionNotActiveError`, 409 / `FAILED_PRECONDITION`).
- **Collection sweeper.** The server finishes collections an interrupted create
  or drop left `creating` or `dropping` for more than five minutes — a stuck
  drop is completed, a stuck create is unwound — so nobody has to re-issue them
  (`server.WithCollectionSweep` tunes it). The shared store's databases are
  found from a replicated collection work journal and swept by the system
  shard's leader (`server.WithCollectionSweepLeader`, wired by the daemon), so
  a database only another node has catalogued is still finished after a
  leadership change; a raft tenant's shard is swept by its leader, and volume
  hosts and node-local dedicated stores on every node that holds them. A
  record whose fields do not derive from one another is reported and left
  alone rather than acted on, and a database with stuck work that stays
  unresolvable is logged as a warning. Deleting a tenant removes its journal
  entries (`__sys:collection-work/{tenant}/`) with the rest of its keyspace.
- **Persisted secondary-index ownership.** Index-create commands carry the
  creating object's owner and index-drop commands can be owner-checked
  (`IndexServicer.DropOwnedIndex`), both as trailing wire extensions that
  existing log entries decode unchanged.
- `CollectionServicer.UpsertDocument`, a guarded insert-or-replace that
  reports what it displaced.
- **Collection index definitions and lifecycle state on every transport.**
  `CreateCollectionRequest` carries `indexes` (and `storage_codec`, which only
  the embedded transport carried before) on REST, gRPC, Unix, QUIC, MCP and
  embedded, and `CollectionInfo` reports `indexes`, `state` and `state_since`.
  The protobuf messages gain `CollectionIndexDefinition`,
  `CreateCollectionRequest.storage_codec`/`indexes` and
  `CollectionInfoResponse.storage_codec`/`indexes`/`state`/`state_since`.
  `qrdb collection create` takes a repeatable `--index FIELD[,unique][,sparse]`,
  and `info`/`list` show the state.

### Changed — BREAKING

- **Relational tables created by v0.20.0 through v0.21.0 must be dropped and
  re-created; there is no migration.** Those releases wrote rows and index entries under
  a key layout that carried neither a table generation nor an index incarnation, which
  `TRUNCATE TABLE` in O(1) and background reclamation both require. Such a table is now
  refused, never read as empty: every read, write and `TRUNCATE` fails with MySQL error
  1459 (`ER_TABLE_NEEDS_UPGRADE`; PostgreSQL SQLSTATE 55000), gRPC/Unix/MCP
  `FAILED_PRECONDITION`, QUIC `StatusFailedPrecondition` or REST 412, naming the table
  and the remedy. `DROP TABLE` still works and reclaims the old data; then `CREATE
  TABLE` it again and reload its rows. A legacy table whose v0.20.x `ALTER` rewrite was
  interrupted is droppable too; `information_schema` lists it with the refusal as its
  comment.
- **DDL on a table refuses while its `ALTER TABLE ... DROP/MODIFY COLUMN` rewrite is
  unfinished.** `CREATE`/`DROP`/`RENAME INDEX`, `ADD`/`DROP`/`UPDATE FOREIGN KEY` and
  `RENAME TABLE` now fail with the rewrite-in-progress error, as `ALTER` and `TRUNCATE`
  already did, instead of committing a change the rewrite's completion would undo. `DROP
  TABLE` still works.
- **Every `--sm` / `raft.sm_type` serves transactions and guarded batches, and
  starts (go-qrdb#136).** `memory`, `standard`, `concurrent`, `fast`, `disk`,
  `highperf` and `regular` now decide `OpTransaction` and `OpBatch` against
  their own keyspace — the one `Put`, `Get` and the guarded verbs use — with the
  default pebble state machine's compare semantics (value and version compares;
  create/mod-revision compares refused with the same error). The start-time
  refusal 0.21.0-alpha added for these types no longer fires, so a deployment
  that stayed on an earlier release to keep one of them can upgrade.
  `configs/qrdb-dev.yaml` is back on `sm_type: memory`.
- **The `mvcc-fast` sm_type is removed, with `pkg/mvccadapter`.** Its MVCC
  store lived in a keyspace separate from the KV plane — a transaction compare
  could not see a plain `Put`, and the transaction service's opcodes were
  refused outright — and it kept that store in a temporary directory. A config
  naming it is refused at load. Use `mvcc` for revisions, or any other type
  for transactions.
- **`statemachine.Backend` gains a required `ApplyAtomic(muts []Mutation)
  error`.** A transaction or batch commits all or nothing through it: one
  `storage.Batch` on `DiskBackend`, one acquisition of every touched shard's
  lock on `FastBackend` and the in-memory backends (`FastBackend` writes
  through to storage asynchronously, in apply order, as `Put` does — its
  durability is the Raft log's, since each replica is rebuilt from the log and
  snapshots at start). A `Backend` implemented outside this repository must
  add it.
- **Under the seal barrier, `--sm disk`/`fast` storage is encrypted at rest or
  refused.** That storage was never wrapped by the barrier, so a replica's data
  sat on disk in plaintext beside a barrier the operator enabled. Over
  `--sm-storage pebble` it is now opened on the barrier's encfs, like the
  `pebble` and `mvcc` state machines. `--sm-storage file` cannot be encrypted
  and is refused at start (`SealedSMStoragePlaintextError`; registry:
  `statemachine.SMStorageUnencryptedError`); `memory` persists nothing and is
  allowed. `sdk/go/node.NewClusteredNode` applies the same rule and no longer
  refuses every non-default `sm_type` under the barrier.
- **Empty keys are refused on every write path, and the disk state machine's
  applied-index key is reserved.** `TransactionService` and `BatchService`
  refuse an empty key before proposing it (`service.InvalidKeyError` inside
  `EncodeRequestError`, invalid-argument on every transport); a state machine
  that receives one anyway refuses it as an application result
  (`statemachine.InvalidKeyError`, a new `protocol.RefusalError` declaration
  the Apply classifier encodes) instead of halting Apply on every replica. A
  client write to `"\x00__qrdb_sm_last_applied__"` is refused on every
  registry state machine.
- **`statemachine.SetDataDirRoot` and `SetSMStorage` are removed.** Storage is
  passed per resolution: `statemachine.ResolveTypedWithStorage(smType,
  smBackend, StorageConfig{Root, Kind})`, and storage-generic state machines
  register through `RegisterStorageGeneric`. `sdk/go/node.NewClusteredNode`
  now honours `raft.sm_storage` (it ignored it), and refuses a non-pebble
  `sm_storage` for a state machine that carries its own storage.
- **The node capability report names the real sm_type.** A raft node's profile
  is narrowed by its state machine's descriptor
  (`statemachine.NarrowCapabilities`): a node on `memory` no longer reports
  MVCC, watch, lease, lock, index or time series, and the default `pebble`
  node no longer reports MVCC, which it never served. Every descriptor —
  the registry's and `store.PebbleDescriptor`/`MVCCDescriptor` — is held to
  what its state machine does by a behaviour test.
- **`server.NewTenantHostRegistryFromServicer` is renamed
  `server.NewTenantHostRegistryFromLookup`** and takes a `server.TenantLookup`
  (any value with `GetTenant(ctx, id) (*tenant.TenantInfo, error)`) instead of
  a `tenant.TenantServicer` (#383). *Migration:* rename the call. A
  `tenant.TenantServicer` still satisfies `TenantLookup`, so an argument that
  is never swapped passes unchanged; a composition that rebuilds its tenant
  service in place passes a lookup that resolves the current one on each call.
- **`server.TenantInfraConfig.TenantLookup` is required** whenever
  `BuildTenantInfra` builds the `IsolationVolume` host registry (`BarrierReg`,
  `TenantSvc` and `VolumePool` all set) (#383). Without it `BuildTenantInfra`
  returns `*server.TenantInfraConfigError{Field: "TenantLookup"}` before any
  side effect. *Migration:* set `TenantLookup` — to the same value as
  `TenantSvc` if that service is never replaced for the life of the process,
  otherwise to a lookup that resolves the current service.
- **`tenant.TenantServicer` gains two methods**, `ListDeletedTenants(ctx,
  pageToken string, pageSize int) (*tenant.DeletedTenantsPage, error)` and
  `ReconcileDeletedTenants(ctx) (*tenant.ReconcileReport, error)` (#470,
  #469), and `transport.TenantClient` gains `ListDeletedTenants` and
  `ReconcileDeletedTenants`. *Migration:* a type that implements either
  interface outside this module must add both methods. A wrapper that embeds
  the interface inherits them. A test double that models no retirements
  returns an empty `DeletedTenantsPage` and a zero `ReconcileReport`.

### Changed

- **`TRUNCATE TABLE` is O(1) regardless of table size.** Relational row and index keys
  carry a generation number; `TRUNCATE` advances it in one transaction and publishes a
  durable reclaim job for the old generation instead of scanning and deleting every row.
  A statement or transaction that raced it fails with MySQL error 1213 / SQLSTATE 40001
  (retry the statement), never with rows from the wrong generation.
- **Index entries are keyed by an index incarnation, not the index name.** An incarnation
  is allocated when the index is created and never handed out twice within a table, so `ALTER TABLE ... RENAME
  INDEX` is a catalog-only change that moves no data, and a new index created under an
  old name never shares a keyspace with the entries being reclaimed for the old one.
- **`DROP INDEX` reclaims its entries in the background.** They are no longer deleted
  inline, so a statement still reading the index is never cut short; one that outlives
  the drop fails with 1213 / 40001.
- **Orphaned keyspace is reclaimed in the background, never under a live reader.** A
  sweeper on every node deletes a truncated generation or a dropped index's entries only
  after every statement that could still be reading them has finished or been failed with
  the retryable error above. Each job is swept by the one node that can reach its store --
  the system leader for the shared store, the publishing node for a node-local store, a
  dedicated Raft group's own leader -- through the same tenant and database view it was
  written through, with bounded work per sweep that resumes where the last one stopped.
- **`DROP TABLE` reclaims its rows in the background.** The catalog entry, generation
  counter and sequences go in one transaction that publishes a reclaim job for the rows
  and index entries, so a statement still reading the table is never cut short, and a
  crash can leave only the whole table or the job that reclaims it. A `DROP TABLE` that
  races a `TRUNCATE` or DDL fails with 1213 / 40001 instead of leaking the new
  generation's rows.
- **The sweeper deletes each page of orphaned keys in one transaction**, one Raft
  proposal per page rather than one per key.
- **A stale table resolution is retryable on every transport.** A statement whose table
  was truncated or whose definition changed while it ran now reaches gRPC, Unix-socket
  and MCP clients as `ABORTED`, QUIC clients as `StatusAborted` and REST clients as 409,
  matching MySQL 1213 / SQLSTATE 40001, instead of an internal error.
- **DML is fenced on the table definition it was planned against.** An `INSERT`,
  `UPDATE` or `DELETE` whose statement resolved the table before a concurrent DDL
  committed fails with 1213 / 40001 instead of maintaining the wrong set of indexes, so a
  new index can no longer miss rows written while it was created.
- **One error classification for every transport.** `pkg/api/errclass` decides what kind of
  failure a typed error is; REST, gRPC (and through it Unix and MCP) and QUIC each render that
  class through one total table of their own. The twelve REST and nine gRPC per-family
  mappers and QUIC's own ladder are deleted, and `TestEveryTransportAgreesOnEveryCondition`
  drives every row of the table through every transport's real mapping. New exported views:
  `rest.ServiceErrorStatus`, `quic.ServiceErrorStatus`, `mcp.ServiceErrorCode`, alongside the
  existing `grpc.ServiceErrorCode`.
- Rows where the transports disagreed now have one answer everywhere (breaking):
  - a system user or role's immutability is 403 / `PERMISSION_DENIED` (gRPC said
    `FAILED_PRECONDITION`), as the identity contract documents;
  - a credential version that is expired or never existed is 404 / `NOT_FOUND` (REST said
    410, gRPC said `FAILED_PRECONDITION` for expired), as the credentials contract documents;
  - removing the last auth factor is 409 / `FAILED_PRECONDITION` (gRPC said
    `ALREADY_EXISTS`), as the auth-factors contract documents;
  - a share claim with no verified client certificate is 401 / `UNAUTHENTICATED` on every
    transport (gRPC said `INVALID_ARGUMENT` from the barrier and tenant-barrier handlers and
    `INTERNAL` from its mapper, QUIC said `PERMISSION_DENIED`);
  - `credentials.ErrInvalidStrategy` and `ErrNilBarrier` are internal faults (gRPC said
    `FAILED_PRECONDITION`).
- Conditions no transport classified, and every transport therefore answered as an internal
  fault, now have a class: a duplicate embedding provider is 409 / `ALREADY_EXISTS`; a
  database barrier operation with no barrier manager is 501 / `UNIMPLEMENTED`; an
  unresolvable reclaim store (reported over whatever its resolver said), a leader transfer
  that did not finish and a shard snapshot export that timed out are 503 / `UNAVAILABLE`; a
  factor migration that left records behind is 409 / `FAILED_PRECONDITION`.
- The REST status, peers, capabilities, shard start/stop/state, topology and rebalance
  handlers answer a service error with its class's status rather than a fixed 500, and a
  malformed `POST /users/lookup` body is a `ReadBodyError` like every other handler's. A
  build gate now fails a REST handler that pairs a fixed status with an error it received.
- **A state machine's refusal keeps its kind** (breaking). `service.SMNotConfiguredError`, the
  one type every non-capability refusal shared, is removed: REST answered it 501 and gRPC and
  QUIC `INTERNAL`, and neither was true of all of it. The encoded result now carries a
  `protocol.AppErrorKind`, and each kind has its own type: an absent subsystem (no lessor, no
  time-series store, create/mod-revision compares on a non-MVCC backend, a transaction on a
  server with no transaction servicer) is `CapabilityUnsupportedError` (501 /
  `UNIMPLEMENTED`), whose message names the missing subsystem (`statemachine: lease not
  configured`, `statemachine: timeseries not configured`) where it said `engine: not open` or
  `pebbledb: store not open` of a store that was open (`engine.NotOpenError`, whose only
  producers those refusals were, is removed); an invalid TTL or time-series point is `CommandRejectedError` (400 /
  `INVALID_ARGUMENT`); a unique-index or lease-ID collision is `CommandConflictError` (409 /
  `ALREADY_EXISTS`); a closed lessor or time-series store is `SubsystemClosedError` (503 /
  `UNAVAILABLE`); an undecodable committed command is `CommandCorruptError` (500 /
  `DATA_LOSS`); any other subsystem failure is `ApplyFaultError` (500 / `INTERNAL`).
  `protocol.EncodeAppError` takes the kind.
- A caller's own deadline or cancellation (`context.DeadlineExceeded`, `context.Canceled`) is
  classified as such on every transport — 504 / `DEADLINE_EXCEEDED` and `CANCELED` — rather than
  as an internal fault of whatever wrapper carried it.
- **The Go SDK's REST client types every 501** as `transport.CapabilityUnsupportedError`
  (inside the `RequestError` that keeps the status), not only the guarded verbs', so the
  go-qrdb#126 exclusion in the conformance matrix is retired: every transport is held to the
  typed capability refusal.
- MCP consults a projected module's own gRPC status only for an error the table does not
  classify, the same order QUIC uses, so a module status can no longer shadow qrdb's own
  classification.
- **go-quicraft v0.11.0-alpha → v0.14.1-alpha** (quic-go v0.62.0 → v0.63.0 with
  it). Two of its breaking changes reach go-qrdb: a Raft member is admitted by
  its node identity alone (below), and every `NewHost` must state its at-rest
  encryption. Every host go-qrdb builds now does — `WithBarrier` where the seal
  barrier encrypts the Raft log as before, `WithoutEncryption` where it did not
  (a seal-disabled daemon, the embedded SDK node, the desktop binding), so no
  node changes what it writes. **Wipe dev data directories before upgrading**:
  go-quicraft changed its WAL chunk and snapshot frame formats in place.
- **A failed `CreateCollection` leaves nothing behind.** It used to publish the
  collection before building its indexes, so a failed index build left a live,
  partially indexed collection that could not be re-created. The create now
  builds the indexes first and unwinds on failure; a retry succeeds.
- **A collection never adopts an index it did not create**, and its drop
  removes only the indexes it owns. A declared index whose name is already
  held by another index refuses the create (409 / `FAILED_PRECONDITION`). Index
  definitions on a service without an index service are refused instead of
  being recorded and silently not built.
- **Collection document writes count against `MaxStorageBytes`**, by body
  bytes, on every transport including SQL. Enforcement is immediate: a tenant
  already over its limit through documents has its next document write
  refused. Every release is of exactly what its own guarded write removed, so
  concurrent updates and deletes never leak or double-release bytes; a drop —
  a caller's or the sweeper's — releases exactly the bytes its teardown
  removed, and a `field_hash` re-insert of an existing natural key is charged
  once, not once per insert. The remaining drift is an over-count from writes
  that race a drop or have an unknown outcome; it is permanent for prefix and
  raft tenants (docs/collections/engineering.md).
- **Each create of a collection keeps its documents and its `increment`
  counter in a keyspace of its own**
  (`__col:{database}:{collection}#{incarnation}/`,
  `__collection:{collection}#{incarnation}`). A collection created before this
  release keeps the unsuffixed keys it already has; no migration is needed.
  A new collection's name may no longer contain `#`
  (`service.CollectionInvalidNameError`, 400 / `INVALID_ARGUMENT`); an existing
  collection with such a name keeps working.
- **A document field named `id`, or an object-valued `fields` as a
  document's only field, is refused** (`service.CollectionReservedFieldError`,
  400 / `INVALID_ARGUMENT`) instead of being accepted, charged against quota,
  and stored as something else -- the `id` field silently dropped, the
  `fields` object unwrapped into the document.
- **`DeleteDocument` refuses a collection that is being created or dropped**,
  like every other document verb (409 / `FAILED_PRECONDITION`); the drop
  removes every document itself.
- **`REPLACE INTO`, `INSERT … ON DUPLICATE KEY UPDATE` and `UPDATE` on a
  collection table are guarded writes.** A concurrent writer's value is never
  lost silently; the losing statement fails with MySQL 1213 / `SQLSTATE 40001`
  and can be retried. An `UPDATE` that changes `_id` and cannot tell whether
  its release of the old `_id` took effect leaves both documents and says so
  (`provider.DocumentMoveOutcomeUnknownError`) instead of withdrawing the new
  copy.
- **Breaking (embedders):** `statemachine.IndexManager.DropIndex` takes
  `protocol.IndexDropOptions`, and an `IndexManager` must enforce the owner
  carried in `IndexCreateOptions.Owner`; `index.IndexServicer` gains
  `DropOwnedIndex`; `service.CollectionServicer` gains `UpsertDocument`, so
  every implementation or test double of it must add the method.
- **The explicit index API can no longer drop an index a collection owns**;
  it is refused with 409 / `FAILED_PRECONDITION`. Drop the collection instead.
  Explicit and pre-ownership indexes drop as before.
- **Upgrade every node before issuing collection creates or drops.** The
  collection lifecycle, index ownership and the owner-checked index drop are
  decided on every replica's apply path; a node still running an earlier
  release ignores the owner on an index command and would apply an
  owner-checked drop unconditionally. Rolling upgrades are safe while no
  collection is created or dropped.
- **A module can no longer silently take over a REST route.** Built-in routes
  used to yield to a module that claimed the same method and pattern — and so
  did a second module claiming the first one's route. Every REST route now has
  exactly one owner: a second claim fails server construction with
  `rest.RouteCollisionError` naming both claimants. The guard lives in qrdb,
  where the live router is, so a host mounting a module needs no copy of the
  built-in route table. **Breaking** for a module that relied on overriding a
  built-in: move its routes under its own prefix. A host that re-registers
  qrdb's built-in routes from its own modules — one known downstream host
  re-registers 22 of them — now fails construction with
  `RouteCollisionError` on upgrade and must drop those registrations.
- **Route identity follows chi.** `middleware.RESTRouteKey` erases path-parameter
  names and keeps regexp constraints, so `/a/{id}` and `/a/{deviceID}` are one
  route to the registry as they are to chi.
- **Direct router registrations are recorded.** A module route registered on
  `HTTPRegistrar`'s embedded router that collides with a permission-recorded
  route fails construction with `rest.DirectRouteCollisionError`, and
  registering after `RegisterHTTP` returns panics with
  `apimodule.RegistrationClosedError`. **Breaking**: a module may no longer
  call `Mount`, `Route`, `NotFound`, `MethodNotAllowed` or `Use` on the
  registrar's router — each serves requests no registration describes — and
  gets `apimodule.UnsupportedRouterMethodError`; serve a subtree as
  `Handle(method, "/prefix/*", perm, h)`. An unmatched request under a module's
  route prefix is refused with 404 instead of reaching the router's fallback.
  New: `apimodule.RouteClaim`, `apimodule.UnsupportedRouterMethodError`,
  `HTTPRegistrar.DirectRoutes`, `HTTPRegistrar.Freeze`,
  `RESTRouteRegistry.RegisterModulePrefix`/`UnderModulePrefix`.

### Removed

- 46 exported error types that no code path returned, each with a passing test that could
  only observe its own `Error()` string (go-qrdb#243), across `pkg/api`, `pkg/api/etcd`,
  `pkg/api/quic`, `pkg/audit`, `pkg/dao`, `pkg/entity`, `pkg/lease`, `pkg/mvcc`,
  `pkg/planner`, `pkg/rag`, `pkg/routing`, `pkg/service`, `pkg/sql/*`,
  `pkg/storage/partition` and `pkg/vector/*`, plus two SDK types nothing produced:
  `transport.BackupVerifyError`, which only an unreachable embedded-transport arm returned
  (backup verification reports a failed check in its result, never as an error), and
  `transport.ImbalanceReportError`, which no transport constructed. A caller matching one with `errors.As` had
  written a branch that could never be taken.

### Fixed

- `DROP TABLE` deletes a table's `TRUNCATE` generation counter in the same transaction as
  its catalog entry, and refuses to commit over a `TRUNCATE` that raced it, so a failed or
  interrupted drop can no longer leave a resolvable table reading generation 0 or a counter
  nothing will ever delete.
- A read-only transaction that first touches a table after a `TRUNCATE` or DDL committed
  since its snapshot fails with 1213 / 40001 instead of returning no rows -- including
  through a table handle resolved before a `TRUNCATE` its snapshot already includes.
- A dropped index can no longer be resurrected, and its incarnation handed out again, by
  an `ALTER` rewrite completing over a definition that changed after the rewrite began.
- A long statement that re-proves its table mid-rewrite checks the index incarnations it
  reads, not only the generation.
- `CREATE INDEX`'s build is fenced on the definition listing the new index, so a `DROP
  INDEX` landing mid-build fails the build instead of leaving orphaned entries.
- `ALTER TABLE ... DROP COLUMN` and a type-changing `MODIFY COLUMN` rebuild every index on
  the column under a new index incarnation. The old entries, which were encoded for the old
  column set, are reclaimed in the background, and so are the entries of an index the drop
  leaves with no columns. Before, the index kept those stale entries or leaked them. A
  `UNIQUE` index narrowed onto repeating values now fails the `ALTER` and leaves the table
  unchanged.
- QUIC answers precisely for every family its ladder never listed — identity, tenants,
  credentials, PINs outside the barrier handlers, auth factors, ceremonies, migrations,
  backup, collections, embedding, RAG, SQL, databases, quotas and placement — instead of
  `StatusError`. Because the Go SDK's QUIC transport rebuilds `transport.KeyNotFoundError`
  from `StatusNotFound`, a missing tenant, credential, factor, officer or backup now surfaces
  to an SDK-over-QUIC caller as `KeyNotFoundError` where it used to be a generic request
  error.
- The relational catalog (tables, views, sequences), typed-entity, paging and SQL-privilege
  errors are classified on every transport: their request guards answer 400 /
  `INVALID_ARGUMENT`, missing objects 404, taken names 409 / `ALREADY_EXISTS`, lost
  compare-and-swaps 409 / `ABORTED`, a table mid-rewrite 503, and catalog corruption 500 /
  `DATA_LOSS`; `service.SubsystemNotConfiguredError` is 501 / `UNIMPLEMENTED`. All of them
  answered 500 / `INTERNAL`.
- gRPC, Unix and MCP classify the backup, embedding, RAG, SQL, database, quota and placement
  families, `seal`'s already-initialized/not-initialized/already-unsealed/invalid-credential
  refusals and `authfactors.ErrHardwareUnavailable`, which fell to `INTERNAL`; REST
  classifies credential re-submission (409), a disabled user record (409), a draining node
  (503) and an invalid ceremony authenticator type (400), which fell to 500. Authentication
  and authorization refusals that reach a service mapper answer 401/403/429/503 on every
  transport, as QUIC's already did.
- gRPC handlers no longer build a status from an error's own text: 161 inline
  `status.Error(code, err.Error())` sites render through the one mapper, and a build gate
  (`test/buildgates/error_mapping_test.go`) fails the next one. The gRPC transport's own
  decode refusals (metadata, vector ID, TTL, document, signature, timestamp) declare their
  class.
- **The vector service's errors reach callers as what they are.** `VectorService` reported
  every failure through the untyped `pkg/errors.QRDBError` code vocabulary, which no transport
  mapped, so 68 of its 100 error sites answered 500: a missing namespace, a dimension mismatch,
  a duplicate namespace and a strict-isolation refusal were all "internal server error". It now
  returns the service's typed errors — `NamespaceNotFoundError`, `VectorNotFoundError`,
  `NamespaceExistsError`, `DimensionMismatchError`, `InvalidVectorError` (all five declared,
  mapped and never constructed until now), plus the new `VectorJobNotFoundError`,
  `VectorServiceClosedError` and `VectorOperationError` — and a storage error is typed by the
  sentinel storage raised rather than by the call site, so a storage fault during a lookup is
  no longer reported as "not found". Breaking for any caller that matched `QRDBError.Code` on a
  vector call; match the types with `errors.As`.
- **Batches on the non-default state machines are all or nothing.** They were
  applied key by key: a concurrent read could see part of one, a crash could
  leave part of one on disk, and the service layer's batch opcodes were
  refused.
- **The `disk` state machine closes its storage when it closes.** It left the
  Pebble store open, holding its directory lock, so a replica restarted in the
  same process could not reopen its own data.
- **The `disk` state machine commits each entry's log index with its writes**
  — single-key guarded verbs as well as transactions — and the `fast` state
  machine starts each replica from an empty keyspace (one storage batch), so
  neither can replay an entry over its own result after a restart and decide
  it the other way.
- **`List` and `Scan` on the `concurrent` and `highperf` state machines never
  see part of a transaction or batch**: they hold every shard's read lock for
  the whole sweep.
- **Clustered nodes form again on go-quicraft ≥ 0.13** (#556). A node with a
  Raft transport admits a member by its node identity (`spki:` + SHA-256 of the
  certificate public key) and by nothing else, and `NewClusteredNode` and
  `qrdbd start` never supplied one, so a multi-node cluster was refused at
  `StartShard`. The other replicas' identities are now configured in
  `raft.member_identities` / `--member-identities` (`replicaID=spki:<hex>`),
  the node's own is derived from its transport certificate, and a local entry
  that contradicts the certificate is refused before anything is written.
  `qrdbd node-identity --cert FILE` and `node.MemberIdentityFromPEM` print a
  node's identity from its certificate; each node also logs its own at start.
  A dedicated per-database Raft shard founded on a clustered host states the
  local replica's identity too.
- **Every add path commits the replica's member identity** (#556). go-quicraft
  commits identities in the shard membership; go-qrdb now reads them from
  there (`SyncGetShardMembership`) and calls the `*WithIdentity` APIs: member
  add, rebalance, dynamic membership, the placement reconciler's followers and
  promotions, founding a multi-replica dedicated database, and the admin
  `StartShard` (which builds `MemberIdentities` as the founders do). **Breaking
  (SDK):** `AdminClient.AddMember`, `AdminServicer.AddMember` and
  `ClusterClient.AddReplica` take an optional identity, carried on all six
  transports and by `qrdb admin member add --identity`; `service.Host` swaps
  `RequestAddNode` for `RequestAddNodeWithIdentity` and gains
  `SyncGetShardMembership` and `NodeIdentity`. A brand-new node needs the
  identity supplied once (a typed refusal says how); one that contradicts a
  committed identity is refused, for the system shard too, and an address the
  committed membership binds to two identities is refused as ambiguous, as is
  a replica ID the membership already places at another address. A
  target shard that exists but cannot be read is a retryable refusal rather
  than a fallback to the host's identity. Rebalance refuses to move the system
  shard (`*service.RebalanceSystemShardMoveError` — every host already hosts
  it) or to add a shard to a host its committed membership already names as a
  voter, observer or witness, picks a replica ID past every one the membership
  has used, and logs every refused move with its typed cause.
- **`node.Node.Close` is ordered against `Unseal` and `EnableEncryption`** (#226).
  Per the owner's ruling, `Close` now blocks on an in-flight boot or
  system-scope migration and then tears down what it built, so a boot can no
  longer republish the data plane after `Close` returned and a migration can
  no longer run against a barrier `Close` retracted. A boot or migration still
  waiting when `Close` begins is refused rather than queued, so a graceful
  server stop inside `Close` cannot wait on a transport-delivered
  `EnableEncryption` that is waiting on `Close`. A tenant-scope migration is
  admitted shared (`server.TenantMigrationAdmission`), so `Close` waits for it
  too and refuses a late one; this supersedes the 0.21.0 `migrateMu`, which
  covered system-scope migrations only. `Close` is idempotent;
  `Unseal` and `EnableEncryption` on a closed node return the new sentinel
  `node.ErrNodeClosed` (previously `EnableEncryption` answered a closed node
  with `*MigrationNotSupportedError`). It is `service.ErrNodeClosed`, which
  every transport answers as retryable — 503 on REST, Unavailable on gRPC,
  unix, QUIC and MCP — where a transport-delivered refusal previously read as
  an internal error.
- **Raft-backed SDK tests no longer charge node boot to the calls under test**
  (#418). Each `sdk/go` raft coordination test took its 60s call budget before
  booting its node, so on a starved host the election consumed it and a lease
  call failed with `quicraft: timeout` — reproduced under load exactly as the
  docs-only PR saw it. The budget (`raftCallBudget`) now starts once the node is
  writable; readiness waits log how long they took and fail as "SLOW, not
  broken" when a leader accepted the write but the commit starved, apart from
  "no leader". The placement-reconciler tests wait for the watch subscription
  and for convergence instead of sleeping 10–30ms and allowing 500ms.
- **A tenant-scope `enable-encryption` works after a system-scope one** on an
  embedded node (#383). `TenantHostRegistry` resolved each tenant's `DataRoot`
  through the first boot's tenant service, which a system-scope migration stops
  and replaces, so every later tenant-scope migration failed with "storage is
  closed". The registry now takes a `server.TenantLookup` consulted at use
  time, and `server.TenantInfraConfig.TenantLookup` is required whenever the
  `IsolationVolume` registry is built. `NewTenantHostRegistryFromServicer` is
  renamed `NewTenantHostRegistryFromLookup` and accepts any `TenantLookup`.
- **`node.Client().Tenants()` works.** The embedded client of an
  `sdk/go/node` node was never given the tenant service, so every
  `TenantClient` verb answered "TenantServicer must not be nil" while the same
  verbs worked on every network transport of the same node.
- **Creating a tenant is atomic against deleting it** (#484). `CreateTenant`'s
  tombstone check was a read and its metadata write a plain put, so a delete
  landing between them left a live tenant record, and for `volume`/`raft` a
  barrier restored on every restart, under a permanently retired id. The
  record is now committed through the new `kvstore.FencedCreator`
  capability (`PutIfAllAbsent`): written only while the record and the
  tombstone are both absent, in one transaction on the replicated store. A
  create that loses to a retirement destroys the barrier it provisioned; two
  concurrent creates of one id have exactly one winner. A metadata store
  without the capability is refused with `tenant.ErrTenantStoreNotAtomic`
  before anything is provisioned. `seal.MemoryBackend`, `seal.StorageAdapter`,
  `seal.StorageBarrier` and `kvstore.MemoryKVStore` implement it; the
  seal-disabled tenant service now runs over a transaction-backed adapter.
- A write into a collection that was being dropped could land after the drop
  had listed its documents and outlive it; document writes are now fenced
  against a concurrent drop and retracted if they lose.
- The schema discovery an insert performs could write a dropped collection's
  metadata back into existence, or revert a drop in progress; metadata writes
  are now guarded swaps.
- A drop that resumed after the collection had been dropped by someone else and
  created again could delete the new collection's documents, restart its
  counter and remove its metadata, and a write decided against the old
  collection could overwrite and then delete a document of the new one. Every
  create now has its own document keyspace and counter, so nothing working on
  an earlier incarnation of a name can reach a later one.
- Listing a collection's documents sorted every key with an insertion sort,
  quadratic in the collection's size; it now uses the standard library's sort.

## [0.21.3-alpha] - 2026-09-30

Security hotfix: tenant isolation on the tenant-administration surface of every network transport (REST, gRPC, Unix socket, QUIC, MCP).

### Changed — BREAKING

- **A tenant's plan is set by the platform operator only (owner ruling
  2026-09-29).** `tenants:create`, `tenants:set_quotas`,
  `tenants:set_entitlements` and `tenants:set_isolation` are now cross-scope
  (`ScopeCross`, `pkg/permission/tenant_provisioning_scope.go`) instead of
  self-scoped: only an identity in the `system` tenant can exercise them. A
  tenant's own Admin keeps managing everything inside its tenant, but can no
  longer create tenants, raise its own quotas, grant itself entitlements or
  change its own isolation tier. The built-in Admin role carries all four in
  every tenant, like its other cross-scope grants, and gains
  `tenants:set_quotas`; outside `system` they are inert. The SO role's
  `tenants:create`, `tenants:set_isolation` and `tenants:set_entitlements`
  grants are cross-scope for the same reason.
- **The quotas operation checks `tenants:set_quotas`, not `tenants:update`**,
  on every transport: `POST /v1/tenants/{id}/quotas`, gRPC/Unix
  `TenantUpdateQuotas` (the `qrdb.v1.permission` proto option), QUIC
  `tenant.UpdateQuotas` and MCP `qrdb_tenant_update_quotas`. A custom role that
  granted quotas through `tenants:update` must grant `tenants:set_quotas`.
- **`tenants:update` covers only the display name.** An update that changes
  `isolation_type` also needs `tenants:set_isolation` and is refused whole
  without it.
- **`tenants:list` returns only the caller's own tenant** unless the caller
  holds `tenants:list:cross` against the `system` tenant. The
  `tenants:list:cross` check is a capability probe: it charges no rate-limit
  token and writes no audit record.
- **`tenants:delete` stays self-scoped (owner ruling 2026-09-29):** a tenant's
  Admin may delete its own tenant, and no other.
- **A tenant can no longer re-scope a built-in permission.**
  `permission.TenantScopeStore.SetScope` refuses a permission that has a
  built-in scope with `*BuiltInScopeOverrideError`, and `ResolveScope` fails
  closed on a stored definition of one. Per-tenant scopes remain available for
  caller-defined permissions.
- **Changing a tenant's isolation type is refused unless the tenant is
  verifiably empty, on every transport.** The check moved from the REST handler
  into the tenant service (`tenant.IsolationChangeWiring`,
  `service.TenantDataProbe`, wired by `server.New`). With no data probe wired,
  or when the probe fails, the change is refused (`ErrTenantDataUnverifiable`,
  `*TenantDataProbeError`: 503 / `UNAVAILABLE`); an embedder constructing a
  tenant service directly must wire one with `tenant.WithTenantDataProbe` or
  `SetTenantDataProbe` to change isolation types. QUIC now answers
  `ErrTenantHasData` with `StatusFailedPrecondition` instead of the generic
  error status.

### Security

- **A tenant's Admin could read, change and delete ANY tenant.** Every network
  transport authorized `tenants:read`, `tenants:update`, `tenants:delete` and
  the quotas and entitlements operations against the CALLER's tenant, where a
  tenant-local Admin holds them, and then acted on the tenant the request
  named. Proven on v0.21.1-alpha with the built-in roles: an `acme` Admin
  answered 200/204 reading, renaming, re-quota-ing, re-entitling and deleting
  `globex`. Each handler now re-decides against the tenant the request names
  (`AuthMiddleware.AuthorizeTenantTarget`, `AuthorizeTenantUpdate`); the
  re-check charges no second rate-limit token and records only a refusal.
- **A tenant's Admin could create tenants and raise its own plan.** An `acme`
  Admin created tenants (201) and set `acme`'s own quotas and entitlements
  (200). Closed by the cross-scope plan permissions above.
- **`tenants:list` disclosed every tenant, `data_root` included, to any tenant
  Admin.** It is narrowed to the caller's own tenant
  (`AuthMiddleware.FilterTenantList`); a refusal the audit policy could not
  record still refuses the request rather than answering it.
- **The barrier bootstrap windows overrode the rate limiter.** The
  trust-on-first-init and sealed-window fallbacks read a `*RateLimitedError` as
  an RBAC denial they could override, so a rate-limit policy on
  `tenant_barrier:init` or `tenant_barrier:unseal` was never enforced for the
  callers those windows admit (a `Burst: 1` unseal policy admitted every
  unseal). Both now return the rate-limit refusal immediately, beside the
  audit-failure check: 429 / `RESOURCE_EXHAUSTED`.
- **A tenant id containing `/` (or `\`, a control byte, `.` or `..`) is a 400 /
  `INVALID_ARGUMENT` on every transport**, checked before authorization on
  every by-id operation. On create it was a 500 / `INTERNAL`
  (`*tenant.InvalidTenantIDCharError` was unmapped); `tenant.ValidateTenantIDShape`
  is now exported for the transports.
- **gRPC, the Unix socket, QUIC, MCP and the embedded transport changed the
  isolation type of a tenant that held data.** Only REST checked that the
  tenant was empty first, so a change through any other transport orphaned
  the tenant's data in its old namespace. The tenant service now enforces it
  for every transport (see above).
- **A tenant could re-scope a built-in permission** through its per-tenant
  scope definitions -- for example back to self-scope for the tenant plan
  permissions. Refused (see above).
- **RBAC resolved an identity's permissions under the request's target
  tenant.** The role store refuses a role outside the context tenant, so a
  `system` operator's first decision against another tenant failed whenever
  the permission cache was cold. The network transports masked it only because
  their entry check, made against the caller's own tenant, warmed the cache
  first. Permissions are now resolved under the identity's own tenant, as the
  SO and Owner role checks already were.

### Fixed

- **Integration fixtures that created users without an email.** Since email
  became the tenant-unique login key, `POST /users` without one is a 400, and
  the raft-isolation, enable-encryption, tenant CLI, SQL and user/role
  integration tests failed at fixture setup before reaching what they test.
  They now pass an email.

## [0.21.2-alpha] - 2026-09-30

Security hotfix: the cluster CA private key never leaves the node that holds it (/v1/ca/bootstrap and every enrollment method serve the certificate only). Treat existing cluster CA keys as compromised; joining new nodes is unavailable until member-side issuance lands.

### Security

- **The cluster CA private key no longer leaves the node that holds it, in
  any form.** Before this release, `GET /v1/ca/bootstrap` served the cluster
  CA certificate and **the CA private key** on every first node of a
  multi-node cluster. The route is public (it is reached before the caller
  holds a credential), so anyone who could reach a first node's admin port
  could read the key and mint any cluster node identity. Every node that
  joined over SPKI-pinned TLS, DANE or direct HTTPS also received the key and
  wrote it to its data directory as `ca-key.pem`. The Noise NK advertiser
  handed the same key-bearing bundle to go-truststrap, whose handler stripped
  the key before it reached the wire. Now `/v1/ca/bootstrap` and the Noise
  advertiser serve the CA certificate only. Any key material handed to them
  is filtered out (`clustertls.CertificatesOnly`), and a joining node never
  writes `ca-key.pem`.

  **Treat the cluster CA key of every existing multi-node cluster as
  compromised.** It was readable without authentication from every first
  node's admin port, and it sits on disk on every joined node. Rotate the
  cluster CA:
  1. Generate a new cluster CA on one node, the one that will hold it.
  2. Issue a new node certificate for every member from the new CA.
  3. Restart the members with a trust bundle holding both CAs, so old and new
     certificates keep verifying during the change.
  4. Once every member presents a certificate from the new CA, remove the old
     CA from the bundle.
  5. Delete `ca-key.pem` from every node except the one holding the new CA.
  6. Reissue client and officer certificates that chain to the old CA.

  Until the cluster CA has been rotated, restrict network access to every
  node's admin port. See `docs/clustering/user.md`, "Adding a node".

### Changed — BREAKING

- **Joining an existing cluster through enrollment is unavailable in this
  release.** A node started with bootstrap flags (`--bootstrap-noise-key`,
  `--bootstrap-spki-pin`, `--bootstrap-dane-dns`, `--bootstrap-direct-url`)
  refuses to start with `*clusterenroll.EnrollmentUnavailableError`
  (`EnrollmentUnavailableError` in `cmd/qrdbd` and `sdk/go/clusterenroll`).
  The refusal comes before any network dial and before anything is written
  to the data directory. Earlier releases enrolled a node by giving it the CA
  private key. Member-side issuance, where the joining node submits a CSR
  that a running member signs, lands in a following release. Until then, add
  a node by issuing its certificate on the node that holds the cluster CA and
  starting it with `--tls-ca-file`, `--tls-cert-file` and `--tls-key-file`.
  This affects `clusterenroll.Join` and the join paths of `Resolve` and
  `ResolveCA`. `ResolveCA`'s join branch no longer returns a CA key, because a
  joining node has none. `FetchAnchor` and the first-node and single-node
  paths are unchanged.
- **`WithCABootstrap` takes the CA certificate only**, in `pkg/api/rest`
  and `pkg/server`: `WithCABootstrap(certPEM []byte)`. The key parameter is
  gone. Input with no `CERTIFICATE` block registers no route.
- **Removed:** `clustertls.GenerateLeafFromCA` and its `sdk/go/clustertls`
  re-export, which minted a leaf from a CA key received over the network.
  Also removed: `clusterenroll.JoinError` (`sdk/go/clusterenroll.JoinError`,
  `cmd/qrdbd.BootstrapError`), which only that path returned.
  `clustertls.CertificatesOnly` is added.

## [0.21.1-alpha] - 2026-09-29

Security hotfix: REST percent-encoded path authorization bypass (authorize and route on the same decoded path).

### Security

- **REST authorization and routing no longer disagree on percent-encoded
  paths.** Authorization resolved the decoded `r.URL.Path`, but chi routed on
  `r.URL.RawPath` whenever the request carried an escape such as `%2F`, so a
  request could be authorized as one route and served by another. Proven on
  v0.21.0-alpha: `DELETE /v1/databases/prod%2Fx` dropped database `prod` for
  any authenticated caller under a deny-all authorizer (the same shape reached
  `/admin/shards/{id}`, `/ts/series/{metric}` and `/users/{id}`); a module route
  authorized under a weak permission could be served by a stronger route's
  handler; and a public route's decoded path could open a protected handler
  with no authentication. The REST handler chain (shared by the HTTP/3
  listener) now canonicalises every request to its decoded path before the
  sealed-window, authentication, SO and routing layers, so all of them resolve
  the same route. Wildcard routes such as `/kv/*` still receive keys with
  encoded characters, decoded, as before; a name containing `/` is not
  addressable through a single-segment route. Upgrade immediately.

## [0.21.0-alpha] - 2026-09-29

Merge train 1: go-enclave protected memory, atomic boot-time identity claims, QUIC qrdb-quic/2, per-tenant auth-factor challenges, PQ k-means empty-cluster repair.

### Added

- **`sdk/go/secret.SealFailedError`** re-exports `pkg/secret.SealFailedError`
  (go-qrdb#114 review), so an SDK-only consumer can `errors.As` against a
  `NewSecretValue` sealing failure without importing the internal `pkg/secret`
  path.
- **`sdk/go/secure.ErrBudgetExhausted`, `ErrLockedMemoryUnavailable`,
  `ErrReleasedWithoutWipe`, `ErrSealerWiped`, `ErrSealFailed`,
  `ErrMaterialTooLarge`, `ErrSealedAuthentication`, `ErrPlaintextEscaped`,
  `ErrInvalidBudget`, `ErrNilAllocator`, `ErrBudgetExceedsRLimit`,
  `ErrLimitDiscoveryFailed`**, and the **`RLimitError`/`LimitDiscoveryError`**
  types, re-export the go-enclave errors this package's own public API can
  actually return (go-qrdb#114 review), so an SDK-only consumer can
  `errors.Is`/`errors.As` against them without importing raw `go-enclave`.

### Changed — BREAKING

- **`pkg/secure`/`sdk/go/secure` no longer wrap memguard; they wrap
  `github.com/jeremyhahn/go-enclave` v0.6.0-alpha (go-qrdb#114, go-qrdb#553).**
  `Buffer` and `Secret`'s own public API (`NewBuffer`, `NewBufferFromBytes`,
  `Bytes`, `WithBytes`, `Size`, `Destroy`, `Wipe`, `NewSecret`,
  `NewSecretFromBuffer`, `Open`) is UNCHANGED — a failed allocation now
  returns a typed, recoverable error (`errors.Is` against
  `enclave.ErrBudgetExhausted` / `enclave.ErrLockedMemoryUnavailable`, or
  `*InvalidSizeError`) instead of memguard's process-wide purge-then-panic.
  `Buffer` gains `DestroyErr() error`, the checked form of `Destroy()` that
  surfaces `enclave.ErrReleasedWithoutWipe` (a denied mprotect during
  release) instead of discarding it — `Destroy()` itself is unchanged and
  still discards it, for the many existing `defer buf.Destroy()` call sites.
  What IS removed, with no replacement in `pkg/secure`:
  - `secure.CatchInterrupt()`, `secure.EnablePurge()`, `secure.Purge() error`,
    `secure.PurgeOrPanic()`, `secure.NotInitializedError` — these belonged to
    memguard's process-signal-driven purge lifecycle, which go-enclave does
    not have. **Migration:** a binary that owned this lifecycle now installs
    a process-wide locked-memory budget once at startup with
    `sdk/go/secure.SetAllocator` (built via
    `sdk/go/secure.NewAllocatorWithinRLimit`) and sweeps it on orderly
    shutdown with `sdk/go/secure.WipeAll() (int, error)` — see
    `cmd/qrdbd/start.go`'s `runStart` for the reference wiring, including
    treating `sdk/go/secure.ErrAllocatorInstalled` as non-fatal and checking
    `WipeAll`'s remaining-enclave count. Neither call installs a signal
    handler; the owning binary keeps driving its own graceful shutdown.
  - `pkg/secure/analyzer`'s rule 1 (no raw `memguard` import) no longer
    exempts `pkg/secure` itself — the import is banned everywhere in this
    repository now, with no carve-out, since `pkg/secure` no longer needs it.
- **`SecretValue.WithBytes` (`pkg/secret`, re-exported by `sdk/go/secret`)
  now holds the transient buffer's borrow for the callback's entire call
  (go-qrdb#114 review).** Previously the callback received an alias whose
  borrow had already been released, so a concurrent `WipeAll` could unmap the
  pages while it was reading them. Now a `WipeAll` during the callback skips
  that buffer and counts it in its remaining total, and, with the borrow guard
  armed (`GuardBorrows` / `EnableBorrowGuard`), a callback that calls back into
  `pkg/secure` / `sdk/go/secure` is reported as a nested borrow. **Migration:**
  a callback must not retain the slice (unchanged), should not call back into
  the secure-memory package, and should not block longer than a shutdown
  sweep can wait for it.
- **`ConfigProcessor.Process` fails closed when a resolved plain-string value
  cannot be read back (go-qrdb#114 review).** It used to write `""` into the
  field and count it resolved; the field now keeps its reference, the path is
  reported `Unresolved`, and the new `secret.ResolvedReadError` (re-exported by
  `sdk/go/secret`) is recorded in `Errors`.
- **An unconfigured `server.secure_memory_budget_mib` is derived from the
  host (go-qrdb#114 review; owner ruling 2026-09-25).** The default is now `0`,
  meaning `qrdbd start` installs the smaller of 64 MiB and `RLIMIT_MEMLOCK`
  less 2 MiB of headroom, and logs the figure and its source. A stock 8 MiB
  container limit therefore starts with a 6 MiB budget instead of failing. A
  limit that leaves less than 1 MiB fails `start` with
  `SecureMemoryLimitTooSmallError`. An explicit budget above the limit still
  fails `start`, and the error now says how to raise the limit. The compose
  files, the devcontainer and the runtime image now ship or document
  `IPC_LOCK` and an unlimited memlock ulimit; see `docs/deployment/`.
  `sdk/go/secure` re-exports `DiscoverLockedMemoryLimit`,
  `LockedMemoryLimit` and `LimitSource`.
- **`secure.NewSecretFromBuffer` seals straight from the input buffer's
  pages** — no intermediate plaintext copy, and no admitted allocation, so an
  exhausted budget no longer makes it fail. The input is consumed on every
  path past the nil/destroyed checks, including a sealing failure.
- **`pkg/secret.NewSecretValue` returns `(*SecretValue, error)`, not
  `*SecretValue` (go-qrdb#114 review).** It used to swallow a
  `secure.NewSecret` sealing failure and return a `SecretValue` whose
  accessors silently reported empty — indistinguishable from a reference that
  legitimately resolves to nothing, so a credential could vanish from
  configuration with no error anywhere. A sealing failure is now
  `*secret.SealFailedError`. `sdk/go/secret.NewSecretValue` is a function-value
  alias and inherits the new signature automatically; every caller — in this
  repository and downstream (go-xkms's `NewLiteralSecretValue` wraps this
  function) — must be updated to check the error.
- **BREAKING: `qrdbd start` refuses every `--sm` / `raft.sm_type` other than `pebble`, `mvcc`
  and `mvcc-fast`, on EVERY start, not only the first.** `memory`, `standard`, `concurrent`,
  `fast`, `disk`, `highperf` and `regular` have no transaction subsystem, and role and user
  creation now commits atomically through one. The check runs right after leader election and
  exits with `auth bootstrap failed at "atomic role/user creation requires an --sm state
  machine with transaction support"`. **An already-bootstrapped deployment on one of those
  types stops starting after this upgrade**, and cannot switch type because its data belongs to
  that state machine: stay on the previous release until that state machine gains transaction
  support. `configs/qrdb-dev.yaml` moves from `sm_type: memory` to `pebble` so it still starts;
  it returns to memory once every state machine supports guarded batches. A follower defers to the
  leader's probe (only the leader can propose); any other inconclusive probe also stops the
  start.
- **BREAKING: the QUIC listener accepts only the ALPN protocol `qrdb-quic/2`.**
  The frames are unchanged; under `qrdb-quic/2` the client closes its side of
  each request stream only after it has read the response, and the server
  waits (bounded) for that close before it counts a request as finished. The
  listener no longer accepts `qrdb-quic`, and the Go SDK offers only
  `qrdb-quic/2` and refuses a connection on which anything else was selected.
  A Go SDK older than this release cannot connect to a current server, and a
  current Go SDK cannot connect to an older server: upgrade both together.

### Fixed

- **Multi-node first boot no longer races on built-in roles and the admin user.** A role or
  user create commits its identity claim, primary record and every secondary index as ONE
  guarded proposal (`kvstore.MultiGuardedBatchKVStore.PutAllIfAllUnchanged`), so a leadership
  change can no longer leave a claim without its record. A role guards its id claim and its
  name together; a user guards its id claim, username and optional email together. First boot
  verifies that a built-in role or admin already created by another node matches the canonical
  definition instead of trusting the collision.
- A lost response on that proposal is reconciled by polling the written keys (every 10 ms, up
  to 5 s) rather than one read, so a create that committed on the cluster but was not yet
  applied locally is not reported as failed and still emits its audit event. A losing create's
  conflict diagnosis polls the same way, so a node reading stale local state still gets the
  typed already-exists error first boot resolves.
- A guard expecting a present, empty value (`kvstore.Guard{Value: []byte{}}`) no longer matches
  an absent key.
- `user.UserIDExistsError` (re-exported as `sdk.UserIDExistsError`) is a conflict on every
  transport: REST 409, gRPC / Unix `ALREADY_EXISTS`, QUIC `StatusAlreadyExists`, MCP
  `CodeAlreadyExists`. It previously fell through to an internal error.
- QUIC answers a taken username (`user.UsernameExistsError`) or email
  (`user.EmailExistsError`) with `StatusAlreadyExists`, as REST and gRPC already did, instead
  of the generic `StatusError`. A failed rollback wrapping any of the three stays
  `StatusDataLoss`.
- **Four production faults in the enable-encryption migration path are fixed.**
  `sdk/go/node`'s `Node.Close` could segfault when it ran concurrently with a
  still-in-flight system-scope encryption migration; it now blocks on the
  same lock the migration holds for its entire recycle, so Close always
  waits for an in-flight migration to finish before tearing anything down.
  The QUIC server could sever a request's connection before it delivered an
  already-decided `"migration_started"` response, reporting "server shutting
  down" instead of the real answer when a migration's synchronous phase
  finished faster than the accept window; it now waits for in-flight
  requests, each confirmed delivered by the `qrdb-quic/2` acknowledgement,
  before closing connections, and ends open watch streams at once instead of
  waiting out its grace period behind them. The recycle could then fail to
  restart the QUIC listener with "address already in use", leaving a
  migrated node without one; the listener now releases its UDP port before
  its stop returns. A node with no seal store could segfault the same way
  on a nil concrete storage handle passed through an interface parameter;
  the seal store is now held as `seal.LegacyStorage` end to end, and a node
  without one refuses before tearing anything down.
- **Enable-encryption errors are classified the same way on every transport.**
  `sdk/go/node`'s `MigrationNotSupportedError` matches
  `service.ErrMigrationProviderUnavailable` through `errors.Is`, so REST
  answers `501` and gRPC, unix, QUIC and MCP `Unimplemented`; REST and gRPC
  previously answered it as an internal error. QUIC now answers every
  migration error (invalid target or option, source not `none`, migration
  in progress, shared-barrier tenant) with the status gRPC gives it instead
  of its generic `StatusError`. A tenant-scope request arriving while a
  system-scope migration is rebuilding the data plane gets the new
  `service.DataPlaneRecyclingError`, `503` / `Unavailable` (retry),
  instead of a refusal, and a failure inside a migration the node accepted is
  the new `MigrationReopenError`, not a refusal.

### Changed

- The routed role/user services an embedded node exposes ask the state machine whether it
  supports guarded batches (`kvstore.ProbeGuardedBatch`) instead of trusting the store's type,
  and only when a role or user is CREATED in the system tenant; reads, lookups and permission
  resolution never send the probe. A definitive "unsupported"
  (`kvstore.GuardedBatchUnsupportedError`, matching `kvstore.ErrAtomicWriteUnsupported`) keeps
  the system tenant on the sequential path; an inconclusive probe fails that one create and is
  asked again.
- A create refused in a way that proves nothing was written (`kvstore.ErrNotApplied`: a
  propose-time `NotLeader`, an unsupported capability) returns at once instead of waiting out
  the 5 s lost-response reconciliation.
- First boot verifies an existing admin by its role, `System` flag and lifecycle, not only its
  username, and retries a generated-id collision with some other user using a fresh id
  instead of aborting.

## [0.20.6-alpha] - 2026-09-25

### Fixed

- A node started with `seal.enabled: false` logs one explicit warning that data at rest is
  plaintext. The warning keys on the configuration decision, not on a missing barrier.
- `enable-encryption` on a seal-disabled node no longer panics the daemon; a concurrent
  second migration request is refused with `seal.ErrMigrationInProgress` (409 /
  `FAILED_PRECONDITION`) instead of tearing down the data plane.
- `enable-encryption` is registered on every transport (REST, gRPC, QUIC, MCP, Unix,
  embedded) and refuses with one typed error where it is unavailable. The embedded, QUIC
  and MCP SDK clients now forward it instead of returning `NotImplementedError`.

### Fixed

- **Enable-encryption reaches the node on every SDK transport.** The embedded
  Go SDK transport now carries real `EncryptionMigrator` wiring from
  `sdk/go/node` (mirroring how the network transports receive theirs), so it
  migrates when a migrator is configured and refuses with the typed
  `service.ErrMigrationProviderUnavailable` only when one truly isn't. The
  QUIC and MCP Go SDK clients now forward `BarrierEnableEncryption` to their
  already-working server handlers instead of returning an unconditional
  `NotImplementedError` stub. The MCP server also registers
  `qrdb_barrier_enable_encryption` unconditionally, closing a gap where a
  standalone node with seal disabled (no barrier service and no migrator)
  returned tool-not-found instead of the typed refusal every other transport
  gives in that state.

## [0.20.5-alpha] - 2026-09-24

### Fixed

- The CI coverage gate no longer fails at random on `pkg/vector/flat`. The insertion-sort
  swap branch in `sortAllResults` was covered only when `sync.Map`'s randomized iteration
  happened to produce an unsorted order (a 1.9-point swing under CPU contention); a
  deterministic test now covers it on every run.

## [0.20.4-alpha] - 2026-09-24

### Fixed

- **k-means++ initialisation no longer hangs.** `pkg/vector/ivf` and `pkg/vector/quantization`
  recomputed every chosen centroid's distance on each selection (O(n·k²·d)); they now keep a
  running minimum (O(n·k·d)). One `DefaultConfig` training call under `-race` drops from ~77s
  to under a second, and the IVF coverage profile is produced again.
- Degenerate training data (fewer distinct points than centroids) always yields exactly k
  initial centroids. Distance overflow (±Inf, NaN) is clamped before weighting, so selection
  never degenerates into Inf/NaN arithmetic. Signed metrics weight by the raw minimum distance.
- `KMeans.Cluster` refuses mismatched vector dimensions with a typed
  `KMeansDimensionMismatchError`.

### Fixed

- **k-means++ initialisation in `pkg/vector/ivf` and `pkg/vector/quantization`
  is O(n·k·d), not O(n·k²·d).** Both implementations picked each centroid by
  rescanning every previously-chosen centroid from scratch; at the shipped
  defaults (256 centroids) that quadratic term, amplified by the race
  detector's per-access instrumentation, drove a k-means test past testing's
  10-minute alarm and killed the test binary before it could write
  `pkg/vector/ivf`'s coverage profile — surfacing in CI as the package
  reporting **missing** coverage rather than a normal test failure. Both now
  track a running minimum distance, updated incrementally against only the
  newly-added centroid.
- **Degenerate k-means++ input (fewer distinct points than the requested
  centroid count) could silently return fewer centroids than requested,**
  violating an invariant every caller downstream assumes. The fallback now
  draws uniformly at random instead of scanning for an "unchosen" point that
  may not exist.
- **The k-means++ running-minimum comparison now uses the metric's raw
  (unsquared) distance, not its square, before taking a minimum** — required
  for signed metrics such as `DotProductDistance`, where comparing squares
  first can pick the wrong centroid as nearest and silently change the
  weighted-sampling distribution.
- **The k-means++ running minimum is clamped against float32 overflow and
  NaN** instead of being assigned directly, so a distance computation that
  overflows to `+Inf` cannot propagate into the weighted-sampling sum. In
  `pkg/vector/ivf`, the sampling weight itself (the clamped minimum squared)
  is now computed in float64, since squaring a large-but-finite clamped
  value — including the clamp's own sentinel, `math.MaxFloat32` — can still
  overflow float32 on its own.
- `ivf.KMeans.Cluster` now validates every vector's dimensionality itself and
  returns the typed `api.KMeansDimensionMismatchError`, rather than letting a
  caller who invokes `Cluster` directly (bypassing `Index.Train`'s own check)
  feed a mismatched vector into the distance functions' overflow-prone
  length-mismatch sentinel.

## [0.20.3-alpha] - 2026-09-24

### Changed

- CI guards every committed generated artifact (coverage baseline, deadcode baseline,
  ifacesweep findings, lockstep baseline) with a per-artifact allowlist. Each
  path-bearing column must be a go-qrdb import path or a repo-relative path. Absolute
  workstation paths, `..` traversal, header-only artifacts and unknown artifacts all
  fail the Lint job.

## [0.20.2-alpha] - 2026-09-23

### Fixed

- **Prefix and range scans no longer walk the whole store.** Metadata keys sort by length
  first, so the keys of one prefix are not one contiguous run, and `List`, `Scan` and `Range`
  read every metadata key in the store and filtered. They now seek once per key length
  (`mvcc.ForEachMetaKeyInRange`) and read only the matching keys. Output order and `Limit`
  are unchanged. go-quicbaas measured ~3.6 s to list 3,648 records before this (#560).

## [0.20.1-alpha] - 2026-09-23

### Fixed

- **`secrets:*` permissions are own-tenant only.** They now use `ScopeSelfStrict`: a tenant's
  principal can reach only its own vault, and the system tenant is confined to its own vault
  rather than lifted over every tenant.
- **Auth failures that could not be recorded are distinguishable from a sealed tenant.**
  `AuthFailureUnavailable` is split into two classes, "could not evaluate" and "could not
  record the decision". They map to the same transport status but can no longer be conflated
  in control flow, and an outer audit-gate failure wins over its inner cause.
- `qrdbd` no longer has a window during signal-handler reset in which a SIGTERM kills the
  process without cleanup.

### Documentation

- `pkg/secret` states its requiredness boundary. A required secret is guaranteed only for a
  leaf the walk reaches; whether a containing section is present is the consumer's `Validate()`.

## [0.20.0-alpha] - 2026-09-23

### Changed — BREAKING

- **`User.Enabled` is removed; `Lifecycle` is the only account-state field.**
  The derived `enabled` boolean is gone from the domain type and from every
  transport — REST, gRPC (`UserResponse` field 5 and the name `enabled` are now
  reserved), QUIC, MCP, the Unix socket and the embedded SDK — and from the
  `qrdb users` output, which shows `Lifecycle` / `LIFECYCLE` in its place. Read
  `lifecycle` instead. A record with no lifecycle state now FAILS CLOSED: it
  cannot authenticate and cannot be transitioned. The decode-time fallback that
  derived a state from `enabled` is removed with no migration; a record written
  before the lifecycle field existed must be recreated.
- **`User.GroupIDs` is removed from qrdb entirely.** It is gone from the
  domain type, `CreateUserRequest` and `UpdateUserRequest`, every transport
  (REST/QUIC `group_ids`/`groupIDs`; gRPC `group_ids` on `CreateUserRequest`,
  `UpdateUserProtoRequest` and `UserResponse` plus `has_group_ids`, all now
  reserved; MCP `group_ids` and `clear_group_ids`), the Go SDK
  (`transport.User`, the request types, and `NormalizeGroupIDs`) and the store's
  audit details. qrdb authorization resolves user → role bindings → roles →
  permissions and no path read the field; a layer that needs group membership
  stores it itself.

### Removed — BREAKING

- **`database.StandaloneBackend.Storage()` is removed** (and with it from the
  SDK's `StandaloneBackend` alias). Writes through the raw store bypassed the
  backend's write mutex, which is what makes its guarded verbs atomic; nothing
  in production called it.

### Fixed

- **A missing lease is reported as not-found on every transport.** Lease
  time-to-live for an absent lease now returns the typed `LeaseNotFoundError`
  from both lessor-backed state machines (`mvcc` and the default `pebble`)
  instead of a generic invalid-query error, and every transport maps it to its
  not-found status. An expired lease is indistinguishable from an absent one.
- `DROP TABLE` reclaims the table's sequence counters.

### Changed

- CI enforces a blocking coverage gate: a repo-wide floor plus a per-package
  ratchet against `test/coverage-baseline.txt`.
- Trivy blocks on every push; the auth integration suite runs per push and the
  cluster integration suite nightly.

## [0.19.0-alpha] - 2026-09-17

MINOR, not PATCH. The release adds a scope to the permission vocabulary and
moves an existing permission family onto it, which changes who may exercise
those permissions — additive for the vocabulary, but a tenancy-reach change for
`escrow:*`, and pre-1.0 MINOR is where surface changes live (release-versioning
convention §1).

This release exists to carry `ScopeSelfStrict` and the `KDFDescriber` seam to
go-xkms: the first closes a cross-tenant escalation on escrowed key material,
and the second unblocks every password-change path in that project.

### Added


- **`permission.ScopeSelfStrict` — a permission confined to the identity's own
  tenant for EVERY identity, the system tenant included.** `ScopeSelf` lifts the
  system tenant across all tenants and `ScopeTenantOnly` denies it outright;
  neither expresses "every tenant is its own boundary, the system tenant
  included" — a permission the system tenant may exercise on its own data and
  on nobody else's. The authorizer gains the matching arm (own tenant admits,
  anything else denies, with no lift), `Scope.String`/`Valid`/`ParseScope` learn
  the `self-strict` label, and the SDK gains
  `transport.PermissionScopeSelfStrict` carrying that same label — the wire form
  is identical whichever layer wrote it, so a persisted per-tenant override
  decodes.

### Changed

- **`escrow:create/list/recover/revoke` move from `ScopeSelf` to
  `ScopeSelfStrict`.** They were liftable, so an identity in the system tenant
  could escrow, list, recover or revoke **any** tenant's key material — a
  cross-tenant escalation wearing an admin role. Each tenant, the system tenant
  included, now reaches its own escrowed material and no other's. The duty
  classification (`DutyGovernance`, never `DutyCryptoUse`) and the built-in role
  grants are unchanged.

- `seal.ChangePassword` identifies the strategy it replaces through the new
  exported `seal.KDFDescriber` interface (`KDFVersion()` + `KDFParams()`)
  instead of asserting the concrete `*seal.SoftwareStrategy`. A strategy that
  composes a software one — the shape a consumer reaches for to add auditing or
  metrics around sealing — carries the very KDF parameters the re-seal needs,
  and was being refused for its type rather than its behaviour. Extending the
  seam is what this repo prescribes when an interface proves insufficient,
  rather than having consumers keep a duplicate strategy of their own.
- `seal.ErrPassphraseStrategyNotSoftware` now states the real condition
  ("does not report its KDF parameters"). The sentinel's name and identity are
  unchanged, so `errors.Is` callers are unaffected; only the message differs.

- **The embedding manager's default provider may be one the embedder
  registers.** `NewManager` validated `Config.Provider` against a literal list
  of the four built-ins, so a config naming an out-of-tree provider — an NPU
  backend, an OpenVINO sidecar through the existing provider bridge — was
  refused with `ErrProviderInvalid` before any provider could be registered.
  The asymmetry was the tell: `RegisterProvider` applies no allowlist, so a
  consumer could register a provider it could not then select as the default.
  The list is gone; an unknown name is answered where it is first used, by
  `*ProviderNotFoundError`. The built-ins' own rules are untouched.

### Fixed

- **The standalone gate refuses with a service-layer error every transport
  classifies.** Under mode `standalone` the raft-only gate refused MVCC, watch,
  lease and lock with `*rest.ServiceNotImplementedError` — a REST transport type
  only the REST transport classified. REST answered 501; gRPC, unix, QUIC and
  MCP fell through to their unclassified default and reported the refusal as an
  internal fault. Those tell a client library opposite things: "the server
  broke, retry" versus "this node cannot do that, fall back". A consumer running
  standalone and calling an MVCC verb was told its server was broken, for a
  deployment mode. The gate, and the per-database registry's "not configured"
  refusals beside it, now return `*service.CapabilityUnsupportedError`, which
  every transport's classifier already maps to "unsupported".

- **Two `pkg/service` skip guards were coverage gaps wearing a green tick.**
  Both claimed the path they abandoned needed real storage; neither claim held —
  the backup registry is an in-memory map in the same package. The tests now
  corrupt a captured shard and assert `VerifyBackup` reports the mismatch with
  both checksums, and that `RestoreBackup(ValidateFirst)` refuses with
  `BackupChecksumMismatchError` before touching a shard.

### Security

- **trivy scans the frontend's dev dependencies; nanoid and postcss bumped past
  three HIGHs.** trivy's npm parser excludes a lockfile's dev dependencies
  unless `--include-dev-deps` is passed, so the blocking Security gate was green
  on three HIGH advisories — CVE-2026-67213 and CVE-2026-67214 in nanoid 3.3.12,
  CVE-2026-73646 in postcss 8.5.15 — that `npm audit` reported and that sat
  squarely at the CRITICAL,HIGH floor. A scope gap, reporting nothing about a
  whole dependency tree. Owner ruling (2026-09-16): dev dependencies are in
  scope on the blocking leg, because vite and esbuild produce the shipped
  bundle and a compromised build tool is a supply-chain path into the artifact.

## [0.18.0-alpha] - 2026-09-16

MINOR, not PATCH. The release adds a permission family to the registry:
new vocabulary is additive for existing callers, but pre-1.0 MINOR is where
surface changes live (release-versioning convention §1), and a consumer that
gates escrow routes on these permissions cannot resolve them from an earlier
tag at all.

This release exists to carry `escrow:create`, `escrow:recover`, `escrow:list`
and `escrow:revoke` to go-xkms, whose escrow subsystem is implemented and
wired but cannot be exposed on any network transport until the permissions it
gates those routes with exist here.

### Added

- **A key-escrow permission family** (`escrow:create`, `escrow:recover`,
  `escrow:list`, `escrow:revoke`). go-xkms's escrow subsystem sends wrapped
  key material to an external custodian (KMIP or a federation agent) and
  reconstructs it back, and every key-material route it exposes is gated by a
  permission pair drawn from this registry — there was no escrow family for
  it to use. Classified `DutyGovernance` (key entry/output, the same duty as
  `keys:import`/`keys:export`, not a cryptographic primitive), scoped
  `ScopeSelf`, and granted in full to the Security Officer and Admin roles,
  with `escrow:list` (metadata only — never key material) also granted to
  Auditor. qrdb owns the vocabulary; it ships no escrow implementation of its
  own, matching the existing PIV/SSH/Passwords split.

## [0.17.0-alpha] - 2026-09-13

MINOR, not PATCH. Of the 99 commits on `main` between `v0.16.3-alpha` and this
release, 20 carry a breaking marker in their subject, and three more (#402,
#429, #485) change an exported interface or contract without one. Pre-1.0,
MINOR is where breakage lives (release-versioning convention §1).

This release exists to carry `pkg/secure/analyzer/scope.go` to consumers. At
`0.16.3-alpha` the analyzer's only non-test source file is `analyzer.go`, so
without the four scoping clauses it reports generated protobuf, `_test.go`
files and wire DTOs as rule-3 findings — a gate wired against the pinned
release is red for reasons no change in the consuming repository can fix.
`scope.go` landed in `6d0c17b` (#429, #450) and has been unreachable to
consumers ever since.

### Added

- **The MCP transport carries the guarded write verbs** (#127).
  `sdk/go/transport/mcp` now implements `transport.GuardedKVClient`, so a
  consumer's `client.KV().(transport.GuardedKVClient)` succeeds over MCP as it
  already did over embedded, gRPC, unix, QUIC and REST (#119/#124/#125/#126).
  Three new tools carry the verbs — `qrdb_put_if_absent`,
  `qrdb_compare_and_delete`, `qrdb_compare_and_swap` — each requiring the
  permission of the write it performs (`kv:put`, `kv:delete`, `kv:put`), each
  taking its values base64-encoded, and each answering with a single-field JSON
  object (`{"stored": …}`, `{"deleted": …}`, `{"swapped": …}`) that mirrors the
  REST response bodies. A refused guard is a SUCCESSFUL tool result carrying
  false, never a tool error: losing the race is the normal outcome of a claim.

  Every handler makes exactly ONE call, to `service.KVServicer`'s matching
  atomic method — no `Get` before it, no read-then-write fallback after it —
  and that is asserted rather than asserted-in-prose. The MCP transport gets
  the guarded conformance matrix's four full-passage cells and the
  cross-transport contention suite's MCP row (`sdk/go/conformance`), plus a
  delegation test that counts which service methods the handler reached.
  Verified by mutation: replacing the single `PutIfAbsent` call with a `Get`
  followed by a `Put` leaves every sequential assertion green and turns both
  the delegation counters and the contention suite red.

  A client with the verbs talking to an MCP server without the tools gets
  `transport.CapabilityUnsupportedError`, matching the version-skew half of the
  contract the other transports carry. The classification is by JSON-RPC error
  CODE, not message text.

  **Known limit:** MCP does not walk the matrix's four typed-refusal cells,
  which require a transport that can present the multi-key transaction plane's
  capability refusal *by type*. MCP reports a service-layer failure as a tool
  result carrying a message string, so that refusal reaches the client as
  prose; admitting it would mean loosening `classifyRefusal` from a type
  assertion back to a substring match. Excluded and named
  (`transportKind.carriesTypedCapabilityRefusal`) rather than papered over;
  #169's error-typing sweep owns that surface.

- **Rate limiting is centralized in the auth middleware** (#201, #394), one
  limiter rather than a decision each handler made for itself.
- **Typed lease not-found and lease-expired across gRPC, unix, MCP and QUIC**
  (#385), so the condition is carried rather than reconstructed from prose.
- **`pkg/secure/analyzer` is armed as a build gate, with rule 3 scoped to
  where it can be satisfied** (#429, #450) — `scope.go`'s four clauses exempt
  generated protobuf, `_test.go` files, flag targets and wire DTOs, which are
  not holders of key material and cannot satisfy the rule. **This is the change
  this release exists to publish.**
- **A Trivy reporting floor below the blocking one** (#522). `make trivy`
  gated on `CRITICAL,HIGH` and that was the only severity list in the
  repository, so no leg could report a MEDIUM and a green Security signal
  could not distinguish "no MEDIUMs" from "nothing looked". Two real
  advisories, both fixed upstream, sat under that floor for the whole of the
  repository's history. The blocking floor does not move; `make trivy-report`
  scans `MEDIUM,LOW` as its own nightly step after the gate, exits nonzero on
  a finding so the signal can go red, gates nothing, and prints `UNKNOWN`
  without failing on it. `test/buildgates/trivy_severity_test.go` pins the
  split by parsing the Makefile and nightly workflow: every severity band is
  named by some leg, the gate names nothing below HIGH, the report leg is
  unreachable from `security`/`ci`/`_ci` transitively, and nightly runs the
  gate then the report. A local review of the first cut found the gate passed
  with the report leg commented out, made transitively blocking, or unplugged
  from nightly, and fixing those found a fourth: a column-0 make comment
  between recipe lines truncated the parse. All four are watched reds.
- **The FIPS document claims only what was measured** (#523). Nightly run
  17302 on `133fa56` is the first *scheduled* `fips140=only` run on `main`
  ever to pass, after twenty-two consecutive failures: root `make test` 126
  packages pass / 0 fail, `sdk/go` `make test-sdk` 18 / 0, with zero
  GCM-arbitrary-IV, X25519, `crypto/sha1` and short-key HMAC rejections.
  `docs/seal/fips140-mode.md` states step by step what that run executed,
  pins the figures to that commit with a statement that the tree has moved
  since, and **withdraws** the earlier claim that `go build ./...` was clean
  for `sdk/go`: the job does not run that step, so the SDK is compiled only
  as far as `go test` compiles it. Arming the job as a required check now
  waits on a stability record, fourteen consecutive green scheduled runs,
  rather than on preconditions that are met.

- **A red nightly opens a tracking issue, a still-red one rewrites it in place,
  and a green one closes it** (#478, #525).
- **`docs/coordination/`** (#173, #404) — the five-document feature folder the
  coordination surface never had.

### Changed — BREAKING

- **A refusal travels in the result payload, and no longer halts the shard**
  (#520, #527). Raft treats any non-nil error out of `Update` as a fatal apply
  failure: `lastApplied` stops advancing, and every subsequent linearizable
  read on that shard — on any engine sharing it, on keys written and read
  successfully microseconds earlier — waits out the caller's deadline and
  reports a consensus timeout. The MVCC engine had **no** apply classification
  at all, and the time-series stub could not say a refusal was a refusal, so
  one refused time-series write cost the shard its reads. Measured, not
  theorised, on both engines (#129 on KV, #520 on MVCC).

  `statemachine.ResolveApplyError` is now THE classification seam for the
  repository, and every `Update` in the tree routes through it. **The error
  declares which it is**: an error implementing `protocol.CapabilityError` —
  an `error` with a `Capability() string` method naming the absent verb or
  subsystem — is a refusal and is encoded into `Result.Data` with a capability
  marker, which the service layer turns into a typed
  `CapabilityUnsupportedError` for the caller. Everything else is a fault and
  still halts Apply. Silence means fatal; only a declaration means benign, so a
  forgotten marker costs availability on one shard loudly rather than
  swallowing a storage fault and serving diverged state silently.

  `test/buildgates` finds every method in `pkg/` taking `[]sm.Entry` and
  requires it to route through `ResolveApplyError` or to be listed, with a
  reason, as a delegator — default-deny, because the absence of a call is not
  something a reviewer reading one diff can see. A refused batch or transaction
  now stages nothing: opcodes are validated before any operation in the set is
  applied. **Reachable by any client able to propose an opcode this state
  machine does not implement.**

- **The MCP transport carries seventeen JSON-RPC error codes, and the
  substring classification is deleted** (#438). `pkg/api/mcp` had **no**
  error-code mapping at all — a tool failure reached a client as a sentence —
  so every typed condition the SDK promises degraded to matching words in
  prose. `sdk/go/transport/mcp` classified four conditions with
  `strings.Contains(msg, "not found")` and one with `"lease expired"`, which is
  fail-open in the direction that costs most: a transport failure whose text
  happened to carry the phrase read as a successful determination of absence.

  The new `pkg/api/mcp/wire` package declares one code per gRPC status code,
  by MEANING, exactly as the QUIC wire does since #437 — so a consumer keeps
  one mental model across transports instead of acquiring a second. The
  numbers are JSON-RPC's, from the −32000..−32099 band reserved for
  implementation-defined server errors, and three of them (−32002, −32005,
  −32010) are deliberately left unassigned to the names the portfolio's error
  convention already gives them.

  | condition | code |
  |---|---|
  | not found (key, user, role, lease, revision) | `NOT_FOUND` (−32004) |
  | barrier sealed, store closed, not leader, draining | `UNAVAILABLE` (−32006) |
  | capability this node was not built with | `UNIMPLEMENTED` (−32014) |
  | throttled caller | `RESOURCE_EXHAUSTED` (−32008) |
  | unclassified server fault | `INTERNAL` (−32603) |

  **What changes on the wire.** A service-layer failure used to leave as a
  JSON-RPC error pinned to `INTERNAL_ERROR`; it is now a tool RESULT carrying
  `isError`, which is what the MCP specification asks for and the only envelope
  a tool handler controls. The text content is byte-identical to what it was,
  so a client that only reads text sees no difference; the code rides in
  `structuredContent` as `{"error": {"code", "status", "message", "tool"}}`.

  **What changes in the SDK.** The nine single-subject user methods return
  `*transport.UserNotFoundError` naming the id or key **the caller supplied**,
  where they previously returned `*transport.KeyNotFoundError` (four of them)
  or a generic request error (five). `transport.RequestError.StatusCode` now
  carries the JSON-RPC code on MCP. An unrecognised code from a newer server
  degrades to the generic request error, never to a misclassification.

  The server owns no classification clauses of its own: it consults the gRPC
  transport's per-family classifiers through the new
  `grpc.ServiceErrorCode` and renders the result, so a condition cannot mean
  one thing over gRPC and another over MCP.

  One message test remains, behind a code gate: the lease expired/absent
  split, which no transport in this repository can separate by code because
  gRPC maps both to `NOT_FOUND`. It fires only once the code has already said
  not-found; it is not the ungated classification this change deleted.

- **Two barrier-passphrase config fields are `*secure.Buffer`, and an unusable
  one is now an error rather than a downgrade** (#429). Arming
  `pkg/secure/analyzer` as a build gate left exactly two findings that were
  neither a wire DTO, a generated struct, a flag target nor a test fixture —
  two Go config structs that a caller fills from its own key source and that
  live as long as the process they configure. Those are *holders* of key
  material, which is the population rule 3 exists for, so both changed type:

  | surface | before | after |
  |---|---|---|
  | `bootstrap.SystemConfig.Passphrase`, re-exported as `sdk.BootstrapSystemConfig` | `string` | `*secure.Buffer` |
  | `embedded.BarrierConfig.Passphrase` (`sdk/go/transport/embedded`) | `[]byte` | `*secure.Buffer` |

  Use `secure.NewBufferFromBytes`; `sdk/go/secure` re-exports the type and its
  constructors, so no consumer needs the internal `pkg/secure` path. The
  caller keeps ownership — neither `BootstrapSystem` nor `NewServer` destroys
  the buffer; each copies out of it once, inside a `WithBytes` borrow, at the
  point the material becomes the `seal.Credentials` the barrier demands.
  `string` cannot be zeroized at all and `[]byte` only by the caller's own
  discipline; a `Buffer` is mlocked, canary-guarded and wiped on `Destroy`.

  **A `*secure.Buffer` has a LIFECYCLE that neither previous type had, and
  that is a behaviour change, not only a type change.** `Buffer.WithBytes`
  returns the same `*NilBufferError` for a nil receiver, a destroyed buffer
  and a zero value, so the obvious single error check reads all three as "no
  passphrase configured". For `embedded.BarrierConfig` that is not a wrong
  error message, it is a **wrong security posture**: on a fresh data dir the
  resulting `seal.StrategyNone` barrier initializes successfully, `IsSealed()`
  is false, `encfs` wraps the Pebble data plane with a root key sealed by
  nothing, and an embedder that asked for a passphrase barrier gets plaintext
  at rest, permanently, with nothing said about it. The two cases are
  therefore separated:

  - `Passphrase == nil` — **not configured**. Unchanged behaviour: with
    `StrategyPassphrase` or the empty default the embedded transport comes up
    with no barrier, which go-quicraft announces at initialize (`barrier
    performs NO encryption`).
  - `Passphrase != nil` but unreadable — **a configuration error**.
    `NewServer` fails with the new `*BarrierPassphraseError` and does not
    fall back. Destroying the buffer after one server and constructing another
    is exactly what the ownership rule above invites, and it is the sequence
    that must fail loudly.

  Covered by four shapes in both directions — nil, destroyed, zero value,
  live — in `sdk/go/transport/embedded/barrier_passphrase_test.go`. Three of
  them are red without the guard; the live case additionally asserts the
  written value is not on disk in the clear. `pkg/bootstrap` keeps treating an
  unreadable buffer as `*MissingPassphraseError`, which is the error that
  branch already returned when the field was a `string`, and no barrier is
  weakened there.

  **Also corrected in the same change:** `BarrierConfig.Passphrase`'s doc
  claimed that with no passphrase `NewServer` "falls back to the deterministic
  bootstrap passphrase keyed by deployment ID 0". No such derivation exists
  anywhere in `sdk/go` — the only thing resembling it is a string a test
  builds and passes to `BarrierInitialize` explicitly — and the code ten lines
  into `setupBarrier` says so itself: "there is no deployment-derived
  passphrase". On a public SDK field whose doc is the only thing an embedder
  reads, the gap between "derives a passphrase" and "comes up with no barrier"
  is the gap between encrypted and not.

  **Consumer verification (pre-publication exception,
  `conventions/release-versioning.md`):** inside this repository the changed
  fields are referenced only by `cmd/qrdbd`, `sdk/go/types_bootstrap.go` and
  the two packages' own tests, all updated here. go-xkms and go-quicbaas were
  not checked out on the host that made this change and could not be grepped;
  both are filed rather than assumed — go-xkms#550 and go-quicbaas#830. Not a
  zero-consumer change and not claimed as one.

- **Barrier credentials are wipeable bytes at every layer** (#184). Every field
  that carries a caller-supplied barrier credential changed from `string` to
  `[]byte`: `BarrierInitializeRequest.Secret`, `BarrierUnsealRequest.Secret`,
  `BarrierInitializeShamirRequest.Secret`,
  `BarrierEnableEncryptionRequest.Passphrase` and
  `TenantBarrierInitRequest.Passphrase` in `sdk/go/transport`; the matching
  `secret` / `passphrase` fields in `qrdb.proto` (`string` → `bytes`); the
  `secret` / `passphrase` parameters on `service.BarrierServicer` and
  `tenant.TenantServicer`; and the REST and QUIC JSON request structs on both
  the client and the server.

  A Go `string` is immutable and therefore **cannot be zeroized**. A consumer
  holding a passphrase in protected memory had to convert it to a `string` to
  populate the request, and that copy then lived on the heap until the
  collector happened to reuse the page — recoverable from a core dump or a
  memory scrape for an unbounded period. The field type was the only thing
  forcing that, and `TenantBarrierUnsealRequest.Key` was already `[]byte`, so
  the fix is to make the outliers match rather than to add anything.

  The type change makes the credential *wipeable*; the rest of the change
  makes it *wiped*. The caller owns the slice it supplies and may zeroize it
  the moment the call returns, because no transport, service or sealing
  strategy retains it.

  **What "retains nothing" does and does not cover.** Wiping the slice destroys
  that slice and every alias of it. It does not reach the transient copy each
  encoder necessarily makes — base64 in a REST body, protobuf's out-of-place
  marshalling, the MCP JSON-RPC body. Those are unreferenced once the call
  completes and the collector reclaims them, but they are not zeroized, and
  nothing at this layer can zeroize them: serializing a value means copying it
  and the wire format owns that copy. The claim is that the copy the CALLER
  holds and the copy the DAEMON decodes are both destroyable on demand, which
  a string never was — not that no plaintext exists anywhere. The daemon owns the slice its decoder produced and
  `secure.Wipe`s it before the handler returns — on the success path and on
  every error path, including a rejected unseal, which is the case that
  matters most. `enable-encryption` is the one deliberate exception: its
  migration outlives the request, so ownership passes to that goroutine and
  the wipe is deferred there instead.

  **Wire format.** proto3 encodes `bytes` and `string` identically, so gRPC,
  Unix and QUIC protobuf frames are byte-for-byte unchanged. REST JSON and the
  QUIC tenant JSON frame now carry these fields as **base64**, the same
  convention every other `[]byte` field on those surfaces already uses. The
  TypeScript SDK encodes and decodes this for its callers; its own API is
  unchanged and still takes plaintext.

  **MCP is the documented exception.** An MCP tool argument is a JSON string
  by definition of the protocol, so `pkg/api/mcp.secretArg` and
  `sdk/go/transport/mcp.secretArg` are single, commented conversion points on
  each side. Callers who cannot accept that residue should not drive barrier
  operations over MCP.

  **Not covered:** Shamir share fields and SO/User PINs are still text strings
  on every transport. They are secrets too; the gap is stated in
  `docs/barrier-seal/api.md` rather than left implied.

  **Consumer verification (pre-publication exception,
  `conventions/release-versioning.md`):** inside this repository the changed
  types are referenced only by `sdk/go/transport/*`, `pkg/api/*`, `pkg/service`,
  `pkg/tenant` and `cmd/qrdb`, all updated here. go-xkms, go-quicbaas and
  go-trusted-platform were **not checked out on the host that made this
  change** and could not be grepped. go-quicbaas is a known consumer — it is
  the repository that raised this issue — and will need a mechanical update at
  its call sites, after which it can delete the `passphraseString` conversion
  it documents as residue today. This is not a zero-consumer change and is not
  claimed as one.

- **`RevokeCert` reports what it did, on every transport** (#287).
  `cert.CertProvider.RevokeCert`, `transport.CertClient.RevokeCert` (`sdk/go`)
  and the gRPC/Unix `RevokeCert` RPC now return a result alongside the error:
  `(*cert.RevokeResult, error)` in `pkg/cert`, `(*transport.RevokeResult,
  error)` in the SDK, `RevokeCertResponse{record_found}` on the wire (the RPC
  previously returned `google.protobuf.Empty`). `RevokeResult.RecordFound`
  (a projection of `RevokeOutcome`) distinguishes a certificate this node
  holds a `CertRecord` for from a deny-by-serial revocation of one it does
  not — the certificate may have been issued by a peer, or its record
  pruned. Both are successes: the revocation marker is written
  unconditionally, before the outcome is even determined, so a mistyped
  serial was previously indistinguishable from a deliberate revocation of a
  peer-issued certificate. Reporting that distinction as an error would have
  misreported the deliberate case — the one deny-by-serial exists to
  protect — as a failure. `qrdb ca revoke` / `qrdb certs revoke` now prints
  which case occurred.

  **Consumer verification (pre-publication exception,
  `conventions/release-versioning.md`):** `RevokeCert` and `ExportPKCS12` are
  referenced only inside go-qrdb's own `pkg/cert`, `pkg/api/*` and
  `sdk/go/transport/*`. go-xkms, go-quicbaas and go-trusted-platform were not
  checked out on the host that made this change; go-trusted-ca matches on
  the method name but has no go-qrdb dependency in its `go.mod`, so its
  `RevokeCert` is an unrelated API, not a consumer of this one. Zero verified
  external consumers at the time of this change.

- **`/certs/*` is gated on the tenant the REQUEST names, not the caller's**
  (#474, #506). `certs:generate` took its `tenantID` from the request body
  unchecked and wrote it into the certificate's Subject OU, which the mTLS
  authenticator reads as the identity's tenant — so a caller could mint a
  CA-signed certificate that authenticates as another tenant. Reachable on four
  transports.
- **The QUIC wire carries gRPC's seventeen conditions, not six** (#437, #461),
  by meaning rather than by message text; four lanes were blocked on the
  missing vocabulary.
- **`LockServicer` resolves per (tenant, database)** (#455, #487). It was a
  process singleton over pre-namespace engines, so two tenants sharing a shard
  under `prefix` isolation took the same lock, and `List`/`Status` enumerated
  each other's keys and holder ids.
- **Collection documents are (tenant, database)-namespaced** (#441, #454), so
  the collection service resolves like its twelve siblings.
- **Auth factors are per-tenant** (#393, #463), routed through
  `tenant.StoreResolver`.
- **The SO-PIN gate fails closed** on the same lever as the authorisation gate
  (#445, #471).
- **A deleted tenant id is retired, not released** (#413, #468).
- **`DROP DATABASE` reclaims the keyspace**, not just the directory (#412,
  #458).
- **`DropCollection` reclaims the collection's ID sequence** (#411, #449) —
  `SequenceServicer` gains a delete verb.
- **An explicit `_id` reserves the counter, and an insert never overwrites
  silently** (#447, #465).
- **A collection table honours the `_id` it declares as its primary key**
  (#467, #475). `REPLACE INTO` never replaced, `ON DUPLICATE KEY UPDATE` never
  updated, and `UPDATE ... SET _id` destroyed the document.
- **`Emit` reports whether the record landed, and an unrecordable
  authorisation decision is refused** (#335, #443).
- **An evicted entity watch says why** (#496, #501) — a terminal variant on
  `WatchEntityEvent`, and the one consumer that would have dropped it.
- **A revoked peer certificate no longer authorizes on the cluster key plane**
  (#285, #390).
- **`gosec`'s `-tags` is comma-separated** (#295, #397); the gate had been
  scanning the untagged tree.

- **A cert binding can be withdrawn** (#518). `AddCertBinding` shipped on all
  six transports and nothing removed a binding on any of them, so a binding
  written today could not be withdrawn through any API. Since #505 made the
  binding set what decides an account's mTLS admission, an enrolled key was an
  irrevocable *credential* rather than a stale record — and revocation is not
  the remedy, because a binding is over the `SubjectPublicKeyInfo` and outlives
  every certificate issued for that key, so a re-issue for the same key
  authenticates again. The exploitability is bounded (re-issuance needs
  `certs:generate`, gated on the target tenant by #506); what it cost was an
  operator who could not clean up after a key compromise.

  `RemoveCertBinding(userID, thumbprint)` is now on all six transports —
  `DELETE /users/{id}/cert-bindings/{thumbprint}`, the `RemoveCertBinding` RPC
  (gRPC and unix), `user.RemoveCertBinding` (QUIC),
  `qrdb_user_remove_cert_binding` (MCP) and `Client.Users()` (embedded) — and in
  the TypeScript SDK. It removes the record entry, the tenant-scoped SPKI index
  entry and the legacy un-scoped one.

  **A narrow verb, not a whole-set replacement.** `UpdateUserRequest.CertBindings`
  already replaced the set and maintained the index; exposing *that* over a
  transport would make a caller's snapshot of the rest of the set part of every
  withdrawal, and committing a snapshot is how a concurrent enrolment
  resurrects a binding that was just withdrawn.

  **`users:remove_cert_binding` is a separate permission**, not folded into
  `users:add_cert_binding`. Enrolment only narrows an account; withdrawing its
  LAST binding widens it back to admitting any certificate whose CN and OU name
  it. Both permissions now declare `ScopeSelf` explicitly rather than inheriting
  it, because `LookupScope` returns `ScopeSelf` for anything nobody tagged and
  these two decide which certificates authenticate as an account.

  **Revocation still does not drop a binding, on purpose.** Revoking during a
  routine rotation would otherwise unenrol the key being rotated. The two are
  separate operations answering separate questions, and an operator cleaning up
  after a key compromise performs both.

- **`ServiceModule` gains `RegisterQUIC`** (#402). A fourth registration hook
  beside `RegisterGRPC` (which serves gRPC and unix), `RegisterHTTP` and
  `RegisterMCP`, so a module registered once reaches every network transport
  instead of every one but QUIC. A type that implements the interface directly
  rather than embedding `BaseModule` must add the method.

- **A first node can advertise cluster enrollment under `GODEBUG=fips140=only`**
  (#485). Noise_NK_25519_ChaChaPoly_SHA256 performs an X25519 handshake, which
  is not a FIPS 140-3 approved key-agreement scheme, so `crypto/ecdh` refuses
  it. `clusterenroll.StartAdvertiser` returned that refusal as an error and the
  node failed to start: **cluster enrollment did not degrade in FIPS mode, it
  did not start.** The nightly `FIPS 140-3` job had reported this correctly for
  eleven consecutive runs.

  Noise is now excluded rather than attempted, on `crypto/fips140.Enabled()` —
  `Enabled()`, not `Enforced()`, matching the call `pkg/ceremony` makes for an
  Ed25519 external officer, because a deployment that asked for FIPS mode
  should not be handed non-approved cryptography just because `fips140=on`
  tolerates it. The ruling splits on the operator's stated intent:

  - **A first node** excludes Noise and comes up advertising the approved
    methods. It never chose a transport — there is no advertise-method option —
    and SPKI-pinned TLS, DANE and direct HTTPS all fetch the same CA bundle
    from `/v1/ca/bootstrap`. Refusing to boot would mean no multi-node cluster
    under FIPS at all, over a method a FIPS deployment could not use.
  - **A joining node that named Noise alongside an approved method** has Noise
    dropped from the truststrap method order; `AutoFetch` is a fallback chain by
    construction, so a preference list is what was given.
  - **A joining node that named Noise and nothing else** is refused at
    configuration time with `*clusterenroll.NoiseFIPSUnavailableError`, before
    the first dial and before anything reaches the data directory.

  **No downgrade is silent.** Both exclusions log at WARN naming FIPS, the
  method dropped and the approved methods to use instead, and the refusal's
  message leads with FIPS. That matters for the message as much as the
  behaviour: go-truststrap wraps a FIPS refusal in its `ErrInvalidKeySize`, so
  what an operator used to see was `noise: invalid key size: crypto/ecdh: use
  of X25519 ...` — a leading clause that sends them after a key-length bug that
  is not there. qrdb now refuses above that call, so the string is unreachable
  from a FIPS deployment; the mis-wrapping is filed upstream.

  **BREAKING, in the sense that matters here: a non-`nil` `Advertiser` now
  means "this node advertises enrollment", not "a Noise listener is running."**
  `cmd/qrdbd` registers `/v1/ca/bootstrap` on exactly that condition, and
  collapsing the two again would leave a FIPS first node unreachable by *every*
  method rather than by one — silently, which is the shape go-qrdb#241 exists to
  keep out of this package. Ask the new `(*Advertiser).NoiseEnabled()` when the
  Noise listener specifically is the question. `clusterenroll.NoiseAvailable()`
  and `*NoiseFIPSUnavailableError` are exported and re-exported from
  `sdk/go/clusterenroll`.

  No test was skipped to get there, and the ruling is asserted in BOTH runtimes:
  `noiseAvailable` is an overridable variable, as `readFileFn` already is, and
  method selection was split into a `buildAutoConfig` that performs no
  cryptography — which is what lets "a non-FIPS node still selects Noise" run
  under `fips140=only` as well as under `fips140=off`. Where a live Noise
  listener became genuinely unreachable, the test asserts the contract that
  replaces one rather than skipping, so a regression that resumed reading
  `noise-key.hex` in FIPS mode fails it.

  Measured, Go 1.26.6, `GOFIPS140=latest GODEBUG=fips140=only make test`:
  **124 pass / 2 fail** against 120/2 before, with X25519 rejections at **0**
  from 15. `pkg/clusterenroll` and `cmd/qrdbd` are green. The two survivors are
  separate pre-existing defects, both filed: `pkg/tenant` has a `cipher.NewGCM`
  test fixture, and `pkg/service`'s test binary does not compile at all (which
  is not a FIPS failure — it fails with FIPS off too). See
  `docs/seal/fips140-mode.md` and `docs/clustering/`. **Both survivors were
  closed later in this release**: the `pkg/tenant` fixture now uses an AEAD
  FIPS admits (#511), and `pkg/service`'s test double gained the `PutIfAbsent`
  its interface requires (#502).

### Changed

- `AGENTS.md` holds the rules and `CLAUDE.md` is the symlink (#376) — a
  symlink is a mode-120000 blob in git, so every plumbing reader of the old
  arrangement got a nine-byte path instead of the rules, silently and with exit
  status 0.
- `vet-tags` is armed in CI (#386). The recorded blocker was an untidy
  `go.mod`; the measured blocker was a missing tag on the
  `sdk/go/gorm/conformance` leg.
- `sdk/go` is under the linter it had never been under (#364).
- **A project's workflows run CI and nothing else.** The label-triggered
  review and auto-merge workflows this range added and iterated (`v7` through
  `v26`) are removed again: QuicBaaS is the sole orchestrator, and a review
  engine duplicated into every repository was the defect, since a fix reached
  one copy and silently missed the rest. Known gap, accepted by the owner:
  until QuicBaaS can post a verdict, no automated review runs here and merges
  need a human.
- **Exported interface methods with no non-test caller are enumerated and
  gated** (#271, #401): `test/tools/ifacesweep` records the findings per tag
  set, and CI refuses a new one.
- Build hygiene: module credentials point at the forge's new address and are
  written once, not three times (#363); `Dockerfile.gpu` names its final
  stage so the GPU integration lane can build (#365); the devcontainer stops
  rebuilding its 6.5GB image on every source edit (#294, #389).

### Fixed

- **A cert-binding write no longer commits a stale set** (#518).
  `AddCertBinding` read the record and then called `UpdateUser`, which read it
  again and guarded on ITS read — so the window between the two reads was
  unguarded, and an enrolment that read the set before a concurrent withdrawal
  committed would swap its stale set in cleanly afterwards, putting the
  withdrawn credential back. Both binding verbs now commit against the bytes
  they computed from (`updateUserAt`), which makes that a LOST swap, and retry
  the lost swap against whatever won — correct because "add T" and "remove T"
  say what to do to a set without depending on what the caller last saw it
  contain. Exhausting the bound reports `*ConcurrentUpdateError`, because an
  operator withdrawing a compromised key must never read a success that did not
  happen.

- **`secure.StdinProvider` no longer leaves the passphrase in an unwipeable
  heap buffer** (#184). It read through a `bufio.Reader`, which staged the
  passphrase in an ordinary heap allocation that was never wiped — in the one
  provider whose entire purpose is to keep passphrases out of unprotected
  memory. It now reads a bounded line one byte at a time straight into an
  mlocked `Buffer`, which is destroyed (and so zeroed) on every exit path
  including the error ones. Two behaviour changes follow: the provider no
  longer **over-reads** past the newline, so a subsequent reader of the same
  stream still sees the remainder; and input longer than
  `secure.MaxStdinPassphraseLen` (1024) is **refused** rather than truncated,
  because silently accepting a prefix would unseal against a credential the
  operator did not type.

- **`ExportPKCS12` refuses a revoked certificate** (#287). It parses the
  supplied `certPEM`, derives the serial with `FormatSerial` (the single
  definition of that format), and refuses with `*CertRevokedError` when
  `IsRevoked` says the serial is revoked. There is no override flag: a
  forensic or archival export of a revoked bundle needs that added
  deliberately, not defaulted. This is an OPERATOR SIGNAL, not a containment
  control — the caller already supplies `certPEM`/`keyPEM`, so refusal
  withholds nothing it did not already have; its value is that the system
  stops silently packaging a credential it will not honour anywhere in the
  cluster. Gives `cert.CertRevokedError` its first producer: both REST (409)
  and gRPC (`FAILED_PRECONDITION`) mappings were previously unreachable
  except through a mock.

- **Teardown is fenced** (#483, #488). `DeleteTenant` makes the retirement
  durable before the irreversible shred, so a failed tombstone write no longer
  leaves a live tenant whose data is permanently undecryptable. `DROP DATABASE`
  stops deleting other tenants' unattributable collection records (#481). The
  fixture uses an AEAD FIPS admits (#511).
- **`StandaloneBackend` is an `AtomicBackend`** (#466, #490), so the collection
  insert is a guarded write and `PutIfAbsent` works on the dedicated-store
  path.
- **The audit chain spans a rotation, and the verifier knows it** (#452, #476).
- **A cancelled watch releases its goroutine, and a watch that ended for a
  reason says so** (#405, #406, #494) — on the embedded transport and the
  namespaced wrapper. The last two sites of the same pattern closed in #515: a
  refused start revision keeps its type (#495), and a cancelled namespaced
  watch releases its goroutines (#499).
- **Secondary-index maintenance is scoped to the keys an index owns** (#410,
  #448).
- **A closed PebbleDB store returns `ErrStorageClosed` instead of panicking
  `pebble: closed`** (#371, #435).
- **`DropCollection` drops the indexes `CreateCollection` built** (#399, #439).
- **`qrdb_watch` stops crediting its cursor from a replay it never delivered**
  (#388).
- **`pkg/discovery` compiles on darwin** (#359) — the `socket(2)` flags move
  behind a platform seam.
- **`secure` holds the borrow across seven traced reads, and the deadlock is
  loud** (#74, #427).
- **`sdk/go/node` resolves the scoped encryption router's tenant leg lazily**
  (#384).

### Security

- **Cross-tenant certificate issuance is closed** (#474, #506) — see
  `/certs/*` above. A caller could mint a CA-signed certificate that
  authenticates as another tenant, on four transports.
- **An enrolled account's mTLS login is decided by its SPKI binding**, not by
  the certificate's CN and OU (#505, #516). Fail-closed per account: an account
  that has enrolled a binding is refused a certificate that does not match it;
  an account with none authenticates as before.
- **`FinishLogin` resolves the factor on the login session's tenant, not the
  caller's `ctx`** (#303, #392).
- **Every request body is bounded** on the REST plane (#378), and a comment
  claiming a bound six handlers lacked is gone.
- **Every workflow action is pinned by commit digest, and it is gated** (#367,
  #407). A tag is a ref its owner can move, and at the time the review job
  held a write-scoped token; that job is gone (see Changed), the pin gate
  stays.
- **Volume-pool paths are validated before they are created** (#292, #366),
  from one shared guard: absolute and canonical, no symlink, and no overlap
  with another candidate in the same request. The request type is
  wire-shaped, so the guard lands before the change that makes it reachable
  from a transport. `gosec` cannot see this sink: G304 covers opens, not
  directory creation.
- **The module-fetch token never touches disk** (#326, #368). The docker
  build's Gitea credentials reach `go mod download` as a BuildKit secret
  sourced from the environment, not from a temp file an EXIT trap was
  supposed to remove and did not on SIGKILL.
- **`gosec` scans the tag-gated files and the `sdk` module** (#377), and the
  91 test-file findings are triaged with the two measured reasons `-tests`
  still cannot go on recorded (#395, #423).
- **`software.sslmate.com/src/go-pkcs12` moves to v0.7.3 in all three
  modules** (#522) for GHSA-mpwr-8vm7-h73f, an authentication bypass in the
  package's decode functions. Measured rather than assumed: this repository
  calls exactly one symbol from it, the `Modern2023` encoder behind
  `ExportPKCS12`, and never decodes a `.p12`, so the bump is hygiene, not
  exposure; the grep is recorded in `docs/certificate-authority/architecture.md`
  beside the dependency.
- **`echarts` moves to 6.1.0 in the frontend** (#522) for CVE-2026-45249, an
  XSS. The major bump was blocked only by `svelte-echarts`, which nothing
  imported; it is removed. The production build, `svelte-check` and the 31
  time-series Playwright tests were run against the new major.
- **`google.golang.org/grpc` moves to v1.83.2 for CVE-2026-84445, and a stale
  trivy database now refuses to certify rather than certifying blindly**
  (#451, #477).

## [0.16.3-alpha] - 2026-09-06

### Added

- **`RoleServicer.ReconcileBuiltInRoles(ctx, tenantID)` — the ONE sanctioned
  writer to a built-in role's permissions** (#354). A built-in role is
  immutable by design, and the guard's own comment recorded the assumption it
  rested on: "there is no built-in permission reconcile path on boot, so this
  refusal costs no existing caller." That expired the first time a permission
  was renamed. Roles are copied out of `BuiltInRoles()` when a tenant is
  provisioned and never touched again, so a tenant created before the rename
  keeps the retired verbs forever and never gains the live ones — while update
  is refused, delete-and-reseed is refused, and create-over-the-top collides
  with the name index. Reconciliation is admitted because its target is not
  arbitrary: it is `BuiltInRoles()` itself, the definition the guard exists to
  protect. Every other caller is still refused. It is idempotent (a converged
  tenant costs ZERO store writes), touches only `IsSystemRoleName` names so
  custom roles survive untouched, and repairs Owner when present without ever
  creating it — Owner is posture-conditional. It must be called AFTER module
  permission registration, because `BuiltInRoles()` reads the registry at call
  time. Reported per role as `ReconcileReport` / `RoleReconcileResult`
  (permissions added, permissions removed, created).
- **`Role.RetiredPermissions` — drift visible on read, before the repair runs**
  (#354). Derived on every read from the live permission registry, listing the
  stored permissions this build no longer defines, and NEVER persisted:
  `marshalRole` is the single seam every store write passes through and clears
  it. Surfaced on `GetRole`, `GetRoleByName` and `ListRoles`, and on every
  transport's role payload. `Role.RetiredPermissionError()` returns the typed
  `*RetiredPermissionError`; no read path returns it as a failure, because a
  listing that failed on drift would hide exactly the roles an operator needs
  to see.
- **`permission.Unregistered(perms)`** (#354) — screens a whole permission list
  against the registry under ONE read lock. Every role read now screens its
  permissions and Owner holds `All()`, so a loop over `IsRegistered` would take
  a global RWMutex per permission on the authorization path.
- **The reconcile verb on every transport and in the CLI** (#354): REST
  `POST /roles/reconcile`, gRPC/Unix `ReconcileBuiltInRoles`, QUIC
  `role.ReconcileBuiltIn`, MCP `qrdb_role_reconcile_builtin`, embedded, and
  `qrdb roles reconcile [--tenant ID]`. It carries `roles:create` rather than a
  permission of its own: reconciliation IS seeding applied to an existing
  tenant, and a dedicated verb would be absent from every role seeded before it
  existed — so nobody would hold the grant needed to repair that exact absence.
  `qrdb roles get` prints a `Retired:` line. Re-exported by `sdk/go`:
  `ReconcileReport`, `RoleReconcileResult`, `RetiredPermissionError`,
  `RetiredPermissions`; and by `sdk/go/transport`:
  `RoleClient.ReconcileBuiltInRoles`, `ReconcileRolesRequest`,
  `ReconcileRolesResponse`, `RoleReconcileResult`, `Role.RetiredPermissions`.

### Fixed

- **`docs/identity-rbac/api.md`'s role capability matrix had drifted from the
  code** (#354). `GET /roles/by-name/{name}`, `GET /roles`,
  `POST /permissions/resolve`, `permission.List` and `permission.Resolve` named
  routes and methods that do not exist; corrected to `GET /roles/by-name?…`,
  `GET /roles/list`, `POST /roles/resolve-permissions`, `role.ListPermissions`
  and `role.ResolvePermissions`.

## [0.16.2-alpha] - 2026-09-05

### Added

- **`service.AttestationRecorder` — the trusted-server path for recording
  what an enrollment ceremony proved** (#342). `AddAttested` / `RotateAttested`
  take a typed `auth.VerifiedAttestation{Level, AAGUID}` and are the ONE
  writer of the server-owned `attest_*` factor-metadata namespace. The
  production user auth-factor service implements it alongside
  `UserAuthFactorServicer`; the in-process host that verified the attestation
  reaches it by type assertion, as it reaches `ActiveFactorChecker`. It is
  deliberately absent from the servicer interface and from every transport —
  no handler maps to it, so nothing on any wire can produce a verified
  attestation. Caller-supplied `attest_*` keys are refused on this path
  exactly as on `Add` (#234's protection is intact), and a level outside
  `none` / `software` / `hardware` is refused with `ErrInvalidAttestation`
  before anything is written. Re-exported by `sdk/go`: `AttestationRecorder`,
  `VerifiedAttestation`, `AttestationLevel` and its three values,
  `Unattested`, `MetadataAttestLevel`, `MetadataAttestAAGUID`,
  `ReservedMetadataPrefix`, `InvalidAttestationError` / `ErrInvalidAttestation`,
  and — missing since #234 — `ReservedMetadataKeyError` / `ErrReservedMetadata`.

### Changed

- **Every `Add` and `Rotate` on the user auth-factor service now records
  `attest_level=none`** (#342). A factor written without a ceremony proved
  nothing, and the server says so rather than leaving the key absent, so a
  record that proved nothing is distinguishable from one written before the
  field existed. The response carries the persisted metadata, stamp included.
  Callers that stamped `none` themselves to get this distinction must stop:
  the key is now refused as input on every path and written by the server.

### Fixed

- **`sdk.UserServicer` now returns `sdk.UserNotFoundError`** (#343).
  go-qrdb#289 (v0.16.1-alpha) moved `sdk.UserNotFoundError` from an alias of
  `pkg/user.UserNotFoundError` to an alias of `transport.UserNotFoundError`
  so every wire transport matched one type — but the re-exported servicer
  kept returning the service type, which the SDK no longer exported, so a
  servicer consumer honouring the `sdk/go` boundary could not classify a
  missing user at all (go-quicbaas's first-boot admin creation failed on
  its email-uniqueness check). `pkg/user.UserNotFoundError` is now an alias
  of the transport type: ONE type, returned by the servicer in-process and
  by every transport, matched by one `errors.As` against
  `sdk.UserNotFoundError`. The embedded transport's service-to-transport
  translation is deleted — there is nothing left to translate. The message
  is `user: not found: <id|lookup>` whichever layer returns it.

## [0.16.1-alpha] - 2026-09-05

### Added

- **`orchestrator:task_readmit` and `orchestrator:task_block`** (#339), beside
  `orchestrator:task_claim` in the broker data-plane `orchestrator` family.
  `task_readmit` returns a blocked orchestration task to the pending queue;
  `task_block` parks a task as blocked (`needs_info` / `blocked` / `escalated`)
  with a reason. Both are `ScopeTenantOnly` like every other verb in the family
  — denied to the system tenant, confined to the identity's own tenant — and are
  placed exactly where `task_claim` is: Operator holds them (and Admin through
  `orchestrator:*`); User and Auditor do not. The family stays qrdb-owned, which
  is what lets go-quicbaas register the two verbs it maps on its own transports
  without forking the resource.

## [0.16.0-alpha] - 2026-09-02

### Added

- **A real account lifecycle on `user.User`** (#196). `Enabled bool` could not
  express the states an identity actually moves through, so every consumer above
  qrdb carried its own — go-quicbaas kept a `BrokerProfile` side-record with a
  state enum plus separate `suspended` and `locked` booleans, an encoding that
  admits contradictions (locked *and* active) with no rule saying which wins.
  `Lifecycle` is now the single source of truth — `pending` · `active` ·
  `suspended` · `locked` · `disabled` · `deleting` — with a fixed transition
  table, and `Enabled` is a derived projection (`Lifecycle == active`) kept
  because it is the field existing clients read. `User.CanAuthenticate()` is the
  check, and it fails closed on a state the build does not recognize.
  `deleting` is **terminal**: a deletion writes it before its first mutation, so
  a credential ceremony that finishes late cannot leave a fresh orphan behind
  (the shape go-quicbaas#290 needs to close its registration-during-deletion
  TOCTOU). Re-asserting a state is a no-op, so a partially completed deletion is
  resumable.
- **`GroupIDs`, `AuthMethods` and `LastAuth` on `user.User`** (#196), settable
  through `CreateUserRequest` and `UpdateUserRequest`. `GroupIDs` stores the
  membership EDGE — qrdb owns no Group entity, exactly as `RoleIDs` holds role
  identifiers without owning the roles; a group's name and the roles it confers
  belong to the layer that performs the authorization consuming them.
  `AuthMethods` is a policy statement of what an account may present, distinct
  from `Factors`, which records what it has enrolled. `LastAuth` is a high-water
  mark that never moves backwards, so overlapping logins written out of order
  cannot roll an account's last-seen time back.
- **`NormalizeGroupIDs` / `NormalizeAuthMethods`** (#196) — the canonical
  identity-set normalizers, re-exported under `sdk/go` beside `NormalizeEmail`.
  Group ids preserve case (opaque minted tokens); auth methods fold case (a
  closed protocol vocabulary).
- **Every new identity field on every transport** (#196). REST, gRPC, QUIC, MCP,
  unix and all six `sdk/go/transport` clients carry `lifecycle`, `groupIDs`,
  `authMethods` and `lastAuth` on both the response and the create/update
  requests. `enabled` collapses five distinct states into one `false`, and a
  field the domain stores while the wire drops it is a field a remote consumer
  cannot see — which is how a consumer ends up keeping its own copy in the first
  place.
- **The clear-versus-leave-alone distinction survives every wire** (#196).
  `nil` leaves a set alone and an empty non-nil slice CLEARS it, and each
  transport carries that in its own idiom: JSON gets it free (absent / `null` /
  `[]`), gRPC needs explicit `has_group_ids` / `has_auth_methods` flags because
  proto3 cannot distinguish an absent repeated field from an empty one, and the
  MCP tools take `clear_group_ids` / `clear_auth_methods` because a flat tool
  argument cannot either. Without them a membership could be granted but never
  **revoked**: an empty list would read as "no change" and the revocation would
  report success while doing nothing.
- **The MCP identifier lists are JSON arrays, not comma-separated strings**
  (#196). A group id is an opaque minted token, so a comma is a character it may
  legitimately contain: CSV split such an id into two memberships and dropped
  blank elements the store refuses, writing a different authorization graph than
  the caller asked for while reporting success. A malformed list is now reported
  rather than read as "leave alone", because quietly not changing an
  authorization list the caller asked to change is the same defect wearing a
  different hat.

### Changed

- **`AuthMethods` is enforced at the authentication gate**, not merely stored
  (#196). `pkg/auth/middleware` now carries the name of the authenticator that
  succeeded and refuses with `AuthMethodNotPermittedError` (403 /
  `PermissionDenied`) when the account's policy is non-empty and does not list
  it. Storing the policy without checking it made method withdrawal fail
  **open**: a user restricted to `webauthn` could still walk in through the
  mTLS authenticator, because the credential was valid and nothing compared it
  against the policy. An empty policy states no restriction, so no account that
  predates the field is affected; and the check applies only where the method is
  observable to qrdb, because a forwarded assertion and a capability module's
  session token carry none it could evaluate. That second limit is carried by an
  explicit flag rather than by an empty name — an authenticator that names
  itself `""` is observed, not unobservable, and conflating the two would have
  let it bypass the policy through the ordinary chain.
- **`DeleteUser` claims the record by marking it `deleting` before the cascade**
  (#196), and the cascade commits against the marked version. Until the mark
  landed the delete had not won the record: another writer could still bind a
  credential or move a handle, and the cascade was sweeping a snapshot that no
  longer described the account. The account is now unauthenticable from the
  FIRST write of the delete rather than the last.
- **A terminal record accepts no mutation at all** (#196). `UpdateUser`, the
  `LastAuth` merge path and `AddCertBinding` all refuse it. Refusing only the
  lifecycle transition left every other field writable, so a certificate could
  still be bound to an account whose deletion had begun — the exact orphan the
  marker exists to prevent. Re-asserting the terminal state stays permitted, so
  a partially completed deletion is still resumable.
- **Lifecycle refusals map to client-actionable statuses** (#196).
  `InvalidLifecycleError` is HTTP 400 / gRPC `InvalidArgument`; a
  `LifecycleTransitionError` is HTTP 409 / gRPC `FailedPrecondition`. Neither
  had a mapping arm, so the commonest instance — an `EnableUser` racing a
  delete — surfaced as a 500 / `Internal`: a server fault reported for a refusal
  the server made deliberately, and one no client could learn to expect.
- **A `LastAuth`-only update is a commutative merge that retries** (#196)
  instead of returning `ConcurrentUpdateError`. `LastAuth` is written on the
  login path while ordinary updates are where an administrator edits roles;
  routing the login through the general path made its timestamp lost — and lost
  *below* the true high-water mark, because the loser computed its max against a
  base the winner had already replaced — whenever the two overlapped. The retry
  is bounded and sleepless, and the merge writes nothing at all when the stored
  value is already at or past the request.
- **`EnableUser` and `DisableUser` are lifecycle transitions**, not boolean sets
  (#196). Both are refused with a `LifecycleTransitionError` on an account
  already marked `deleting` — a plain `Enabled = true` would have revived an
  account whose deletion was in flight. Repeating either verb is still a no-op.
- **Records written before this release need no migration** (#196). A decode
  with no `lifecycle` derives one from `Enabled` (`true → active`,
  `false → disabled`) and re-derives `Enabled` from it, so no pre-upgrade
  account is locked out by an empty state and the explicit field is picked up
  the next time the record is written. A state that is present but unrecognized
  — written by a newer node mid-upgrade — is left exactly as written.

### Fixed

- **`docs/identity-rbac/engineering.md` had drifted** (#196): its `User` struct
  still showed a `string` ID and no `Email`. Corrected alongside the new fields.
- **`sdk/go/go.mod` required a stale root** (#196). It named
  `github.com/jeremyhahn/go-qrdb v0.10.0-alpha` while the tags had reached
  v0.15.0-alpha. Its `replace ... => ../..` keeps the repository building, but a
  `replace` in a dependency module is ignored by consumers, so an external
  module taking `sdk/go` alone resolved a root five releases behind it. Bumped
  in the release commit that cuts this version, and verified against a throwaway
  external consumer.
- **The release notes for 0.13.0-alpha, 0.14.0-alpha and 0.15.0-alpha were never
  written** (#196), so `make check-version-reconciled` — the check that exists
  precisely to catch this — had been failing on `main` since v0.13.0-alpha:
  VERSION read `0.15.0-alpha` while the newest CHANGELOG release heading still
  read `0.12.0-alpha`. The three sections are reconstructed below from the
  release commits and tag annotations, which restores the check to green.

## [0.15.0-alpha] - 2026-09-01

Notes reconstructed for a release that shipped without them (see the
[Unreleased] entry for #196, which found the gap). Content is taken from the
release commit and the tag annotation, not re-derived.

### Added

- **`Email` is the canonical tenant-unique login key** (#191). `User.Email` and
  `User.Name`, a tenant-scoped email secondary index, and `GetUserByEmail`
  across REST, gRPC, QUIC, MCP, the SDK and the CLI.

### Changed

- **`CreateUser` refuses a user with no email login key** unless the caller sets
  `CreateUserRequest.WithoutEmailLoginKey` (#191). A certificate-only or
  bootstrap identity is legitimate; an ordinary user silently created with no
  entry in the index every login resolves through is not. **Breaking.**

## [0.14.0-alpha] - 2026-08-31

Notes reconstructed from the tag annotation; see the note under 0.15.0-alpha.

### Added

- **Deferred tenant-admin enrollment seam** (#189, #193).
  `AdminEnrollmentMode` on `ProvisionTenant` — `cert` (default) or `deferred`
  (pending-invite) — with fail-closed pending-admin validation including a
  canonical-factor-store check (`service.ActiveFactorChecker` /
  `HasActiveFactor`), and `user.CreateUserRequest.Pending`.
- Registration-config ownership documented (it belongs to quicbaas
  `iam.self_service_registration`, not here).

## [0.13.0-alpha] - 2026-08-28

Notes reconstructed from the tag annotation; see the note under 0.15.0-alpha.

### Added

- **Coordination surface for control-plane consumers** (#166, #168) —
  lock / lease / CAS / watch. Hard acknowledged-deadline lease-loss model,
  `LeaseLostError` on late acks, a terminal-error contract on `WatchStream`, and
  bounded cleanup revoke.

### Fixed

- **`dek_accesses` counts reads, not attempts** (#164) — the counter is
  incremented inside the borrow.
- **`ChangePassword` leaves the barrier still checking passwords** (#161).
- **`withCurrentDEK`'s purge borrow is held across the callback**, with both
  escapes fenced (#82).

### Security

- Bumped echo to v4.15.3 for CVE-2026-55677 (#155).

## [0.12.0-alpha] - 2026-08-25

The guarded-verb plane: the conditional writes that arbitrate a cross-process
race are now reachable on every engine, in both deployment modes, and over the
remote transports — not only through the embedded server. A 12-cell conformance
matrix is the gate that says so, and it found two real defects on its way in.

### Added

- **`CompareAndSwap` on the transport boundary** (#121). `service.KVServicer`
  has carried all three guarded verbs since #56, but only `PutIfAbsent` and
  `CompareAndDelete` reached `transport.GuardedKVClient` in #119. A consumer
  holding the capability could arbitrate a create and a release and still had to
  read-then-write to *edit* — reopening the window the capability exists to
  close, over a network round trip on a remote transport. Implemented on the
  embedded and gRPC transports; QUIC, unix, REST and MCP are tracked as
  #124–#127.
- **The guarded verbs over gRPC** (#119, #123): `PutIfAbsent` and
  `CompareAndDelete` as RPCs, delegating to the service layer's atomic
  primitives with no client-side emulation.
- **`transport.GuardedKVClient`** (#119) — a capability sub-interface detected
  by type assertion, so a transport that cannot honour the verbs does not
  promise them.
- **`CompareAndSwap` and transactions on the file KV engine** (#117, #120), at
  proven parity with the MVCC engine.
- **A 12-cell conformance matrix** (#121): `{file, memory, pebble}` ×
  `{standalone, raft}` × `{embedded, remote-gRPC}`, asserting identical guarded
  semantics on every cell. It runs in-process in the ordinary test pass rather
  than forking the daemon, so a regression turns something red the same day.
- **`kvstore.GuardedBatchKVStore`** (#21) — a capability sub-interface for
  applying several writes atomically, conditional on one key still holding an
  expected value. Detected by type assertion, like `AtomicKVStore`; a store
  without a transaction plane does not advertise it.
- **A typed capability refusal** (#141). `transport.CapabilityUnsupportedError`,
  produced on every transport, so a consumer can detect "this node was not built
  to do that" with `errors.As` and degrade gracefully instead of parsing an
  error string.

### Changed

- **A capability refusal answers `Unimplemented` over gRPC, not `Internal`**
  (#141). `Internal` means the server broke; a client library alerts on it and
  retries an operation that can never succeed. REST answers 501 for the same
  condition, as it did before. **Consumers keying on `codes.Internal` for this
  condition must update.**
- **Registry capability descriptors now describe what the state machines can
  actually do** (#135). The `memory`, `standard`, `concurrent`, `fast`, `disk`
  and `highperf` state machines declared `mvcc`, `transaction`, `lease`, `index`
  and `timeseries` and could perform none of them — every registry factory
  constructs with zero options, so those subsystems are nil. Those flags are
  removed, and `regular` gains the `batch` capability it always had.
  **`sdk.SMDescriptor` consumers will see different `Capabilities` values.**
  Nothing in the database gates an operation on these flags; they are
  informational, and they were misinforming.

### Fixed

- **An unimplemented opcode no longer wedges the raft shard** (#129).
  `KVStateMachine.Update` returned `InvalidOpcodeError` to Raft, which treats a
  non-nil `Update` error as a fatal apply failure and trips the shard's circuit
  breaker. Measured: one ordinary authorized `CompareAndDelete` — an opcode no
  state machine in the package implements — permanently removed the shard from
  service. Every later proposal failed, including plain `Put`, and a
  linearizable read never returned at all. Reachable through the SDK's own
  guarded verbs with nothing more than `kv:delete`. Such an operation is now
  refused with a typed error and the shard keeps serving.
- **A batch naming an unimplemented operation is refused whole** (#129). The
  batch handlers applied operations in turn and only refused on *reaching* the
  unknown one, committing the earlier writes and their watch notifications while
  reporting the batch refused. Opcodes are now validated before anything is
  applied.
- **Transactions are served over the remote transports in standalone mode**
  (#121). `sdk/go/node` composed the transaction service only on the raft path,
  so `ExecuteTransaction` worked over the embedded transport and returned
  "transaction service is not configured" over gRPC on the same node — the
  engine/mode/transport non-uniformity #117 rules out.
- **A custodian enrolment is written as one guarded transaction** (#21). Its
  three keys — the primary record, the tenant index, and the username-index
  commit — are applied atomically, conditional on the registration still owning
  the username claim. Claims expire, so a registration that stalled past the TTL
  could previously lose its claim between an ownership check and its write, land
  that write on top of the winner's, and leave the winner's published index
  pointing at the loser's record: two callers returning success with different
  sealed envelopes, one record surviving, nothing reporting it.
- **A claim is released with a compare-and-delete** (#21), so seal-failure
  cleanup can no longer remove a claim another registration has legitimately
  taken over in the meantime.
- **Shamir share and secret custody moved into protected memory** (#76).
- **A data race in the QUIC e2e index-flags test harness** (#131), which could
  also make its polarity assertion pass for the wrong reason.

### Changed — BREAKING (sdk/go)

- **`IndexScan` expresses an open bound** (#115), so a one-sided range returns
  its matches. A consumer building index scans must update its bound
  construction.

### Scope of the guarded plane in this release

Stated explicitly, because "identical on every cell" has a boundary:

- **Standalone** — all three backends serve the full guarded plane on both the
  embedded and gRPC transports.
- **Raft, default `sm_type`** (`""`, `pebble`, `mvcc`) — full guarded plane.
- **Raft, non-default `sm_type`** (`memory`, `standard`, `concurrent`, `fast`,
  `disk`, `highperf`) — `PutIfAbsent` works and is genuinely atomic, being a
  native KV opcode. `CompareAndSwap`, `CompareAndDelete` and transactions
  **refuse with a typed error and the node keeps serving**. Those state machines
  never had a working MVCC store, and they are refused outright when the seal
  barrier is enabled. Giving them one is tracked as #136; when it lands, those
  matrix cells flip from refusal to full passage.
- **The file engine refuses `create_revision`/`mod_revision` transaction
  compares.** This is designed behaviour, not a limitation to be worked around:
  the engine keeps no MVCC revisions, so a revision compare would read 0, two
  concurrent writers would both compare equal and both commit, and one update
  would be lost with no signal. The native `PutIfAbsent` verb works there, so no
  capability is unreachable — only the etcd revision idiom.
- **Remote transports** — embedded and gRPC. QUIC, unix, REST and MCP do not
  carry the guarded verbs yet (#124–#127); their `GuardedKVClient` assertion
  correctly reports false until they do.

### Internal

- CI now runs the `sdk/go` module's tests and requires them in the gate (#132).
  They had never been executed by CI, so the entire guarded-verb transport plane
  this release is built on had merged on green runs that never exercised it.

## [0.11.0-alpha] - 2026-08-24

### Added

- **Standalone-mode transactions and compare-and-swap** (#62, #109). The
  standalone server now serves the transaction plane and CompareAndSwap, so a
  single-node deployment has the same conditional-write primitives a cluster
  does. This is the primitive `xkmsd` needs to fresh-boot on a qrdb backend
  (unblocks go-xkms #61).
- **`kvstore.CompareAndSwap`** (#81) — the guarded edit the atomic set was
  missing: replace a value only when the current value matches the expected one.
- **`sdk/go` `GuardedSwap`** (#78) — an optimistic-CAS loop over the transaction
  plane that retries a read-modify-write until it commits with no conflicting
  writer.
- **Write results now report whether a failed write was stored** (#103), stated
  at the layer that knows rather than inferred by the caller.
- **`sdk/go` re-exports `UsableAsMapKey`** (#68), so a consumer can guard a
  per-store cache without reaching past the SDK boundary.

### Changed — BREAKING (sdk/go)

- **Index creation carries case-sensitivity end to end** (#34, #108). The
  `sdk/go` index-create surface (`kvstore_adapter`, every transport's
  `transport_index`) gains a case-sensitivity flag and its request shape
  changes. A consumer pinning `sdk/go` must update its index-create calls.

### Fixed

- **A guarded write is durable before it acknowledges** (#94, #110). The state
  machine persists the write before returning success, so an acknowledged
  guarded write survives a crash.
- **Built-in roles are actually immutable** (#105): the `System` flag is derived
  from the role name rather than trusted from the stored record, so a built-in
  role cannot be made editable by clearing the flag.
- **Vector IVF clustering is seedable** (#99), and the recall test is fixed at
  its source — the single shared random init — instead of by averaging
  downstream of it.
- **The quota reservation is kept on every ambiguous write, not just the swap**
  (#98): a write whose stored/not-stored outcome is unknown no longer leaks
  quota.
- **The cert watcher is released on every `runStart` return, not only the clean
  one** (#97).
- **Snapshot framing is bounds-checked through the same predicate the encoders
  use** (#80).
- **`OpPutIfAbsent` is applied everywhere, and first boot reports its real
  error** (#87).
- **A version condition on an entity with no version is refused, not
  fabricated** (#75).
- **MVCC recognises the absence spelling every storage engine actually
  produces** (#77).
- **A failed compare read stalls the transaction instead of silently picking a
  branch** (#64).
- **An empty index tag is a registration error, not a silent no-op** (#67).
- **A not-configured subsystem is encoded so its reader can decode it** (#70).
- **A DAO miss is reported as NOT FOUND over both stores the SDK ships** (#65).
- **One bounds-checked length writer in the protocol**, so the omission cannot
  compile (#53).

### Fixed — SECURITY

- **Purge-class memguard calls are coordinated against live borrows** (#72), so
  a purge cannot free memory another goroutine is still reading.
- **The unseal ceremony fails closed on an unreadable share and holds the PIN
  borrow across the KDF** (#91).

## [0.10.0-alpha] - 2026-08-20

### Fixed — SECURITY

- **`StoreResolver` no longer hands every shared-prefix tenant the same
  unconfined store** (#41). It returned `r.shared` itself for the system
  tenant, every `IsolationPrefix` tenant and every unknown tenant, with no
  keyspace prefix applied. A `dao.GenericDAO` keys entities as
  `{entityType}/{id}` and has no tenant concept, so two prefix tenants' DAOs
  occupied one undifferentiated keyspace: measured on a two-tenant node, one
  tenant's `Count()` returned another's rows and one tenant's `QueryByIndex`
  returned another's rows.

  The data-level exposure pre-dates v0.8.0-alpha; the index-level exposure
  became reachable in v0.8.0-alpha, which made those index paths real.

  `Resolve` now returns a store confined to `{tenant}/` — the tenant alone.
  This store serves IDENTITY (users, roles, permission scopes, invitations),
  and identity belongs to a tenant rather than to one of its databases: a user
  must authenticate whichever database a request names. Namespacing it per
  database was measured to break login outright, because firstboot writes the
  admin user with no database bound to its context.

  `tenant.ComposeNamespace` remains the DATA plane's `{tenant}/{database}/`
  formula, applied by the transports and the SQL provider; that scoping is
  unaffected.

  Either way the isolation is a property of the store rather than of every
  caller remembering to prefix.

  Concrete consequence: `invite.TenantStore` documented that "a redemption
  attempted against the wrong tenant resolves to a different store and returns
  ErrNotFound". That was true for barrier tenants and **false for prefix
  tenants**, whose invitations shared one keyspace — and `Entity.TenantID` was
  written but never compared, so nothing else caught it. An invitation issued
  for prefix tenant A was redeemable through tenant B. The fix is what makes
  the documented guarantee true; the doc comment now records the correction.

### Changed — BREAKING (key layout)

- **Every record a shared-prefix tenant reaches through `StoreResolver` moves
  under `{tenant}/`.** This includes the reserved `__sys:` keyspace —
  users, roles, permission scopes — and DAO-keyed entities alike. There is one
  tenancy mechanism, the namespace, and no prefix exempt from it.

  **Pre-release policy** (`release-versioning.md` §1): backward compatibility,
  migrations and upgrade paths are non-goals before public release. The key
  layout changed clean. **Wipe and re-bootstrap dev deployments** — that is the
  upgrade path. Nothing is "orphaned"; the old layout simply ceases to exist.

### Fixed — regression introduced by the isolation above

- **Every IAM operation that names a record by its globally unique ID now
  reaches the tenant that HOLDS it** (#41). Confining each tenant's keyspace
  broke `users delete <id>`, `users get/enable/disable <id>`,
  `roles delete <id>` and `roles assign --role <id>` for any record created
  with `--tenant-id`: the by-id methods routed on the CALLER's tenant, and the
  admin's tenant is `system`, so the record was looked up in a store it does
  not live in. Measured on the integration stack: five CLI suites failing
  identically across all five transports with `user: not found` /
  `role: not found`.

  They worked before only by accident — every prefix tenant shared one
  keyspace, so "the caller's store" and "the record's store" were the same
  store. That accident is exactly what #41 removes, so the by-id route is now
  made explicit.

  `tenant.IdentityIndex` is a system-plane id → tenant map living in the
  system tenant's identity namespace — `tenant.IdentityNamespace`, the same
  single formula, at `__sys:identity_idx/tenant/{kind}/{id}`. An entry carries
  the id and the tenant and **nothing else**: it necessarily sits outside a
  barrier tenant's encryption, so it may leak existence and placement, never
  a username, role name or permission. It is written and removed by the same
  functions that write and remove the record — `CreateUser`/`DeleteUser`,
  `CreateRole`/`DeleteRole` — alongside their other index writes, because a
  mapping kept in step by a sibling caller is how every instance of this class
  began.

  The entry is a CLAIM, not a write. `CreateRoleRequest.ID` is
  caller-supplied — it travels from the REST and gRPC surfaces straight into
  the store — so a last-writer-wins map would let one tenant create a role
  carrying ANOTHER tenant's role id, take over that id's entry, and silently
  redirect every by-id operation an administrator performs on it. `Record`
  now refuses an id ANY tenant already holds — the claimant's own tenant
  included, because a create proceeding on a claim it did not win can have
  that claim released out from under it by a concurrent same-tenant delete,
  leaving a record with no entry and no way to reach it by id. A claim also
  requires a store that can settle it in one operation
  (`kvstore.AtomicKVStore`); there is no read-then-write mode, because two
  proposers would both observe "absent" and both win. `Record`
  refuses a held id with `*tenant.IdentityClaimConflictError` (
  surfaced to callers as `*role.RoleIDExistsError` → 409 / `AlreadyExists`,
  naming the CALLER's tenant so the refusal does not disclose where the id
  lives), `Forget` releases only a claim the named tenant holds, and the claim
  is settled BEFORE the record is written so a refused capture leaves nothing
  behind. `CreateRole` additionally refuses a duplicate id within the tenant,
  closing a pre-existing silent overwrite.

  `tenant.OwningTenant` is the one read-path rule both routers share: an
  unindexed id routes to the caller's own tenant (reach never widens); an
  indexed id routes to its owner when the caller may reach it; and a caller
  who may NOT reach it is routed to its own tenant, where the id is genuinely
  absent — a `CrossTenantDeniedError` there would confirm to a caller who
  supplied only an id both that the record exists and where it lives. A sealed
  owning barrier surfaces `tenant.ErrTenantSealed`; there is no fallback read.

  No CLI or API surface changes. `role.ResolvePermissions` is deliberately NOT
  index-routed: it resolves the caller's own bindings during authorization, and
  routing it globally would let a binding naming another tenant's role import
  that role's permissions.

- **`pkg/engine.KVEngine` applies `kv.OpPutIfAbsent`.** The opcode already
  existed in the command format, `kvstore.AtomicKVStore` advertised it, and
  `service.KVService.PutIfAbsent` proposed it — but of the three state
  machines only `PebbleStateMachine` (the `qrdbd --sm pebble` default) and
  `MVCCEngine` had a handler. On `--sm memory`, `--sm concurrent` and every
  `pkg/server/lifecycle` embedding, a conditional write failed the **entire
  proposal** with `engine: unsupported operation: 0x04`. The handler mirrors
  its two siblings exactly: the existence check runs inside the apply path so
  every replica decides identically, a declined write is reported through
  `Result.Value` (`kv.ResultNotStored`) rather than as an error that would
  stall consensus, secondary and compound indexes are maintained on the stored
  path, and the watch notification carries no `PrevKV` because the guard
  established the key was absent. All three implementations are now pinned by
  the same contract, including "of N conditional puts to the same key, exactly
  one stores, under every apply grouping".

- **`kvstore.AtomicKVStore` gains `CompareAndDelete`, and every state machine
  implements it** (#41). `PutIfAbsent` guarded the create; the release was a
  plain delete, and a guarded create paired with an unguarded delete is half a
  primitive. Two releasers both read holder A, the first deletes, tenant B
  legitimately claims the now-absent id, and the second delete removes **B's**
  claim — leaving B holding a record with no routing entry, the state the
  index exists to prevent, reached through the release path. No amount of
  re-reading closes it, because the window is between the read and the delete.

  New `protocol.OpCompareAndDelete`, handled by `KVEngine`,
  `PebbleStateMachine` and `MVCCEngine` alike, with the comparison evaluated
  inside the apply path so every replica decides identically. A refused delete
  is reported through `Result.Value`, never as an error that would stall
  consensus; a failed guard READ refuses rather than deciding, exactly as on
  the conditional-put path. `tenant.IdentityIndex.Forget` now releases through
  it.

  Encrypting wrappers (`seal.StorageBarrier`, `seal.atomicTenantSharedKVStore`,
  `database.BarrierBackend`) guard on the **ciphertext they read**, not on a
  re-encryption of the expected value: encryption is randomised, so a
  re-encrypted guard would refuse every time, while the observed ciphertext
  still refuses whenever anything wrote in between.

  One carrier genuinely cannot: `seal.StorageBackend` is a type alias for
  go-quicraft's interface, which has `PutIfAbsent` but not its release half.
  Those two call sites discover the capability and refuse with
  `seal.ErrGuardedDeleteUnsupported` rather than degrading — every backend in
  this repository implements it.

### Added

- `tenant.IdentityIndex`, `tenant.NewIdentityIndex`,
  `tenant.NewResolvedIdentityIndex`, `tenant.OwningTenant`,
  `user.WithIdentityIndex`, `role.WithIdentityIndex`,
  `tenant.IdentityClaimConflictError`, `role.RoleIDExistsError` — see above.
  The Option form keeps the index out of single-tenant embeddings, which have
  nothing to route.
- `kvstore.NewNamespacedKVStore` — a `KVStore` decorator confining every
  operation, including index registration and queries, to a key prefix. Only
  the variant over an atomic inner store implements `AtomicKVStore`, so the
  capability assertion answers honestly. An empty prefix is an error rather
  than a pass-through.
- `kvstore.EntityIndex.KeyPrefix` and `ResolvedPrefix` — the key prefix an
  index covers. Stores previously assumed `{entityType}/`, which holds only
  while the entity type is the LEADING key segment; a namespaced store breaks
  that and would silently index nothing.

### Known limitation

- **Shared-prefix tenants trade engine-backed indexes for isolation — the
  system tenant included** (#40 restores them). `KVEngine` decides which
  indexes a write maintains via `entityTypeFromKey`, which returns the FIRST
  path segment — so a namespaced key `acme/system/workitem/1` reads as entity
  type `acme`. The adapter refuses the namespace-qualified index name and the
  store refuses subsequent index queries with `kvstore.ErrIndexUnsupported`.
  Query RESULTS stay correct: `pkg/dao` detects the refusal and plans a full
  scan, so only the access path is slower. Teaching the engine about
  namespaced keys is #40's priority-1 mechanism.

## [0.9.0-alpha] - 2026-08-20

MINOR rather than PATCH: no qrdb API moves here, but `go-quicraft` advances TWO
minors and changes cryptographic behaviour on the AES-GCM data path, which this
module had to absorb at one call site.

### Changed

- **`go-quicraft` v0.6.2-alpha to v0.8.0-alpha**, `go-objstore` v0.2.2-alpha to
  v0.3.0-alpha. `go-truststrap` (v0.2.0-alpha) and `go-codec` (v1.4.0) were
  already current.

- **The wrapped-root-key envelope absorbs go-quicraft's AEAD nonce contract
  change.** In v0.7.0-alpha `pkg/seal` moved to `cipher.NewGCMWithRandomNonce`
  — the only GCM construction approved under `GODEBUG=fips140=only`, since SP
  800-38D requires the module itself to own nonce generation. Measured at the
  two pins:

  | | v0.6.2-alpha | v0.8.0-alpha |
  |---|---|---|
  | construction | `cipher.NewGCM` | `cipher.NewGCMWithRandomNonce` |
  | `aead.NonceSize()` | 12 | 0 |
  | `aead.Overhead()` | 16 | 28 |

  `pkg/seal/unsealkey.go` was the one place in this repository that took a raw
  `cipher.AEAD` from that package and framed its own nonce: `wrapRootKey` sized
  a nonce from `aead.NonceSize()` and persisted it in the `Nonce` field of the
  `wrappedRootKey` JSON envelope, beside `Ciphertext`, and `resolveDirectSecret`
  length-checked that field against `aead.NonceSize()` before `Open`.

  **A straight bump would have been silent and would have cost the store.**
  Nothing fails to compile — `cipher.AEAD` is unchanged — and a fresh barrier
  works, because new writes and new reads stay consistent with each other. But
  the write path starts emitting a ZERO-length nonce with a 60-byte ciphertext
  where the format has always carried 12 and 48, and the read path rejects every
  blob written before the bump with `ErrWrappedRootKeyCorrupt`. That blob is the
  barrier root key wrapped under the unseal key: an existing barrier stops
  unsealing. Measured on a real pre-bump blob, not inferred.

  The fix keeps the persisted bytes IDENTICAL in both directions rather than
  migrating them. `NewGCMWithRandomNonce` emits `nonce ‖ ciphertext ‖ tag`,
  which is exactly the pair the envelope already stored in two fields, so
  `wrapRootKey` splits the module-generated prefix back off and `unwrapRootKey`
  re-joins it before `Open`. Old blobs open unchanged, new blobs are
  indistinguishable from old ones, and **no migration is required** — which is
  the point: a wrapped root key is written once at initialization and read on
  every unseal for the life of the barrier, so a format change here would need a
  migration executed while the barrier is still sealed, i.e. while the key
  material needed to authenticate it is by definition unavailable.
  `pkg/ceremony`'s `sealEnvelope` splits the same prefix for the same reason;
  this is that pattern, not a new one.

  The envelope's nonce-length check now reads `SymmetricAlgorithm.NonceSize()`
  — the WIRE width, which stayed 12 — instead of `cipher.AEAD.NonceSize()`,
  which is now zero and would accept a nonce field of any length at all.

  Decoding and decryption of the envelope moved out of `resolveDirectSecret`
  into `unwrapRootKey`, so the format rules have one home and are reachable from
  a test without standing up a storage backend.

### Added

- **`pkg/seal/unsealkey_test.go`** gains tests that open a wrapped-root-key blob
  produced by this repository at go-quicraft v0.6.2-alpha, checked in as bytes
  rather than regenerated. Regenerating it would let the test drift onto the new
  format and start asserting only that the new code reads its own output — which
  is precisely the failure mode that makes this class of break invisible. It
  also asserts the write path still emits the 12/48 field widths, that the AAD
  still binds the blob to its `RootKeyPath` (sealing with one barrier and
  opening with another, so the assertion cannot pass merely because the AAD
  changed), and that a nonce field of the wrong width is rejected. All
  mutation-verified.

### FIPS 140-3 posture — the caller-supplied-IV class is GONE

Measured on both sides of this bump from the SAME base commit (414291c), one
script, one machine, Go 1.26.6, `GOFIPS140=latest GODEBUG=fips140=only`,
`-race`, and the build tags `make test` uses. Measuring both sides rather than
comparing against the figure recorded for `0.7.0-alpha` is deliberate: a count
taken by a different run on a differently loaded machine is not a baseline, and
masked findings make counts move for reasons that have nothing to do with the
change under test.

| | before (go-quicraft v0.6.2-alpha) | after (v0.8.0-alpha) |
|---|---|---|
| packages passing | 106 | **120** |
| packages failing | 16 | **2** |
| `use of GCM with arbitrary IVs is not allowed in FIPS 140-only mode` | **699** | **0** |
| `use of X25519 is not allowed in FIPS 140-only mode` | 15 | 15 |

**Fourteen packages move red to green, and there are zero regressions** — the
before-failing set is a strict superset of the after-failing set.

The movement is attributable, not merely coincident: every one of the 699
rejections was the AES-GCM-with-caller-IV construction that go-quicraft
v0.7.0-alpha replaced with `cipher.NewGCMWithRandomNonce`, and the count is
exactly zero afterwards. That whole class is closed for this module.

The X25519 count is *unchanged at 15*, which is the correct outcome rather than
a disappointing one: it is a different finding with a different owner. The two
packages still failing — `cmd/qrdbd` and `pkg/clusterenroll` — fail on
`clusterenroll: bootstrap server (noise) failed to start: noise: invalid key
size: crypto/ecdh: use of X25519 is not allowed in FIPS 140-only mode`. That is
go-truststrap's Noise handshake, closable only by compiling its `pkg/noiseproto`
`fips` variant, which this module's FIPS job does not pass `-tags fips` to. The
same tests fail identically before the bump and pass in the non-FIPS run.

Re-measured at the branch head after the last commit touching test code, not
carried forward from the earlier A/B run: **120 pass / 2 fail / 0 GCM / 15
X25519**, the same figures and the same failing pair. The re-run was not
ceremony — tests added after the A/B briefly introduced `cipher.NewGCM`
fixtures that broke `pkg/seal` under FIPS and invalidated these counts until the
hosted review caught it. A measurement describes the tree it was taken on.

**Two tests now skip under `fips140=only`, and neither is a masked failure.**
`TestWrappedRootKey_CallerNonceProvider` and
`TestWrappedRootKey_RejectsMismatchedProviderNonceWidth` construct a
caller-nonce AEAD to exercise the caller-nonce branch of
`sealWrapped`/`openWrapped`. A caller-nonce AEAD is one built with
`cipher.NewGCM` — exactly what this enforcement level refuses — so that branch
is provably unreachable in a FIPS deployment: no provider, software or hardware,
has an approved way to return such an AEAD. Both run in full outside FIPS. The
skip is keyed on `fips140.Enabled()` rather than a build tag because the FIPS
job passes no extra tags, and it is a skip rather than a stub AEAD because the
assertions that matter need real AES-GCM on both sides. Registered in
`docs/seal/fips140-mode.md` under "Skipped tests" with the same reasoning, so a
future count does not read them as hidden red. (Five tests skip on that run in
total; three predate this change.)

Nothing here claims this module operates under `fips140=only`; two packages
still fail and the job stays out of `ci-gate` while they do.

### Not affected

- Every other AEAD site in this repository — `pkg/crypto/aesgcm`,
  `pkg/dek/crypto.go`, `pkg/ceremony/external_officer.go`,
  `pkg/custodian/share_envelope.go` — already constructs
  `cipher.NewGCMWithRandomNonce` from the standard library directly and passes a
  nil nonce. None of them goes through go-quicraft's provider.
- go-quicraft v0.8.0-alpha's `MemoryEnclave` borrow discipline (`WithBytes`,
  `WithValue`, mutators draining borrows, no callback re-entry) reaches nothing
  here. This module's entire use of `pkg/enclave` is `enclave.WipeBytes`, a free
  function over ordinary slices, and its Shamir use is entirely the string
  `Share` API — no `ProtectedShare`, `SplitProtected` or `CombineProtected`.
  `pkg/secure`'s `enclave` field is this repository's own `memguard.Enclave`
  wrapper, unrelated to go-quicraft's type of the same name.

## [0.8.0-alpha] - 2026-08-19

MINOR, not PATCH: the `kvstore.KVStore` index contract changes (a store that
cannot index MUST now return `ErrIndexUnsupported`; `(nil, nil)` is
forbidden), `EntityIndex` gains fields, and `seal.NewTenantSharedKVStore`
returns a type that no longer always satisfies `AtomicKVStore`. Pre-1.0, MINOR
is where added API and breakage both live.

### Fixed

- **`QueryIndex` no longer lies on `StoreResolver`-returned stores** (#31, #32).
  Every `kvstore.KVStore` a consumer could reach implemented `QueryIndex` as
  `return nil, nil`, and `pkg/dao`'s query planner built its index catalog from
  the entity's struct tags by reflection rather than from what the store could
  serve. A field tagged `index:"true"` therefore routed to `QueryIndex` and came
  back with **zero results and a nil error**, while the same field WITHOUT the
  tag fell back to a full scan and was correct — so adding the index is what
  broke the query, and it failed toward "nothing found" with nothing logged.
  - `lifecycle.kvStoreAdapter` — the store the system tenant resolves to — now
    maintains **real** secondary indexes by routing registration and lookups to
    the node's `IndexServicer`, which reaches the same `KVEngine` that applies
    its writes. Index entries are maintained inside the Raft-applied batch,
    backfilled on registration, and replicated.
  - Stores that genuinely cannot index (`seal.tenantSharedKVStore`,
    `seal.tenantBarrierKVStore`, `embedded.serverKVAdapter`, the `cmd/qrdb`
    and `cmd/qrdbd` KV service adapters when built without a registerer or
    lookup, and any adapter built without an index subsystem) now return the
    new typed `kvstore.ErrIndexUnsupported` instead of an empty result. So do
    the definitions a store cannot serve even when it can serve others: a
    COMPOUND definition on any adapter whose compound query methods refuse, a
    case-insensitive one on the engine-backed paths (#34), and a
    database-qualified name the engine will not maintain (#38). Registration
    success is a capability statement, so it may not be given for something
    the store could never answer.
  - `pkg/dao` probes the capability at construction, **per index kind**. A
    refusing store yields an empty planner catalog for that kind, so tagged
    fields are answered by **full scan and are correct**; a direct
    `QueryByIndex`/`ScanByIndex`/compound call fails closed with the typed
    error. Single-field and compound definitions are registered in separate
    calls because a store can serve one and not the other — the lifecycle
    adapter has real single-field indexes through the engine and no compound
    verb at all — so a compound refusal no longer costs a working single-field
    index, and a compound query over a store without compound support falls
    back to a scan rather than erroring. `dao.New` no longer discards the
    result of `RegisterEntityIndexes`: any non-capability error now fails
    construction.
  - **`WithDatabase` DAOs registered an index name they never queried.**
    `pkg/dao` resolves a database-scoped index as
    `{database}.{entityType}.{field}`, but every store composed the
    unqualified `{entityType}.{field}` itself, so such a DAO advertised an
    index, planned a lookup, and queried a name nobody created. `EntityIndex`
    gains an `IndexName` field carrying the caller's resolved name, which
    every store now uses verbatim via `EntityIndex.ResolvedName` — except the
  engine-backed `lifecycle.kvStoreAdapter`, which REFUSES a qualified name
  (tracked as #38). `KVEngine.filterIndexes` decides which indexes a write
  maintains by matching the name against the key's entity type, and the
  adapter cannot see the engine's database setting — so a qualified name would
  be created, never updated, and answer every query empty with a nil error.
  Refusing makes a `WithDatabase` DAO over a lifecycle store full-scan, which
  is correct.
  - The SDK transport adapter composed `entityType + "." + ei.FieldName` for a
    COMPOUND definition, where `FieldName` is empty — creating a garbage index
    named `{entityType}.` while reporting success. It now refuses compound
    definitions.
- **`seal.tenantSharedKVStore` no longer advertises atomicity it may not
  have** (#30). `kvstore.AtomicKVStore` is optional and discovered by type
  assertion precisely so a caller can fail closed at CONSTRUCTION, but a
  single wrapper type that always defined `PutIfAbsent` made that assertion
  succeed unconditionally — a guard that always passes is not a guard. The
  wrapper is now two types, and only the one over an atomic shared store
  carries the method.
- **`seal.tenantSharedKVStore.PutIfAbsent` no longer degrades silently** (#30).
  When the backing shared store was not an `AtomicKVStore` it fell back to
  `Exists`-then-`Put` — the exact TOCTOU the capability exists to eliminate,
  delivered while reporting success, so two nodes could both win a
  create-if-absent race. It now returns `kvstore.ErrAtomicWriteUnsupported`.
- **The system tenant has an atomic path again** (#30).
  `lifecycle.kvStoreAdapter` had no `PutIfAbsent` at all, so
  `Resolve(ctx, SystemTenantID)` returned a store with no atomic write even
  though the engine beneath it has a native compare-and-set. It now implements
  `kvstore.AtomicKVStore` by delegating to `service.KVServicer.PutIfAbsent`.

### Added

- **`kvstore.MemoryKVStore` / `sdk.NewMemoryKVStore`** (#29) — a supported
  in-process `KVStore` with genuine single-process atomicity and **real** index
  maintenance on the write path. Consumers can unit-test DAO code without
  booting a node and without hand-writing a twelve-method index-aware fake per
  repository. Unique indexes are enforced on the write path
  (`*index.UniqueViolationError`), so a test store never accepts a duplicate
  the real engine would reject; `CaseSensitive` and `Sparse` are honoured
  rather than accepted and ignored (a case-insensitive index that never
  normalized would answer a differently cased probe with an empty result, and
  a unique one would admit both `A` and `a`); and a registration whose
  backfill fails publishes no index at all rather than leaving a half-filled
  one visible. A value it cannot DECODE under an indexed key prefix is now
  refused (`UnindexableValueError`) rather than stored unindexed: storing it
  reported successful index maintenance that did not happen, leaving the
  record invisible to every index query and outside every unique constraint,
  both with nil errors — reachable simply by configuring a DAO with the
  MsgPack or CBOR codec this change re-exports. Index READS hold the store's
  read lock for the whole lookup rather than just long enough to fetch the
  index pointer: a `Put` updates an index in several steps, and a reader that
  released the lock early could observe the key under neither the old term nor
  the new one and report "no matches" with a nil error. The document `null`
  is rejected too: it unmarshals into a nil map WITHOUT error, so it passed as
  a decodable object and then produced no index entries — the record existed
  under an indexed prefix and was invisible to every index query. An empty
  object `{}` remains acceptable, being a real object that simply carries no
  value for the field.
- **`sdk.AtomicKVStore`** (#28) — SDK consumers can now make the fail-closed
  capability assertion at construction without importing `pkg/kvstore`, which
  `docs/00` L3 forbids.
- **`sdk.ErrKVIndexUnsupported`, `sdk.ErrKVAtomicWriteUnsupported`** (#28) — the
  typed refusals, reachable with `errors.Is` from a consumer.
- **The missing DAO surface at `sdk/go`** (#28): `DAOWithShard`, `DAOWithCodec`,
  `DAOWithTimeout`, `DAOWithPartition`, `DAOWithDatabase`; `DAOCodecType` with
  `DAOCodecJSON`/`DAOCodecMsgPack`/`DAOCodecCBOR`; `DAOSortDirection` with
  `DAOSortAscending`/`DAOSortDescending`; and `DAOUnitOfWork`,
  `DAOTransactional`, `DAOVersioned`, `DAOTimestamped`. They carry the `DAO`
  prefix because the SDK root already has `ClientOption` namesakes —
  `sdk.WithTimeout` is a `ClientOption`, so an unprefixed re-export would fail
  at every call site with a type mismatch.

### Changed

- `kvstore.KVStore`'s index-method contract is explicit: a store that cannot
  index MUST return `ErrIndexUnsupported`; `(nil, nil)` is forbidden because it
  is indistinguishable from a maintained index with no matches. A store that
  cannot serve a definition MUST refuse it at registration rather than skipping
  it and returning nil — skipping tells the caller an index exists.
- `RegisterEntityIndexes` is documented as ACCUMULATING per entity type rather
  than replacing, which `pkg/dao`'s split single/compound registration relies
  on.
- `kvstore.EntityIndex` gains `IndexName` and the `ResolvedName` helper.
- The five `TestKVStoreAdapter_*_NoOp` and five `TestTenantBarrierKVStore_*_NoOp`
  tests asserted the `(nil, nil)` behaviour and passed, which is why the defect
  survived — a green test declared it the intended contract. They now assert the
  typed refusal, or are replaced by tests that assert engine-backed answers.

### Known limitations

- **`index:"case_insensitive"` is refused on the engine-backed and transport
  paths** (tracked as #34). `protocol.EncodeIndexCreate` carries no
  case-sensitivity flag, so `KVEngine.handleIndexCreate` builds every index
  case-sensitive; accepting the definition would create a case-SENSITIVE index
  while reporting success, and a lookup for `"Alpha"` over a stored `"alpha"`
  would return empty with a nil error. `lifecycle.kvStoreAdapter` and the SDK
  transport adapter therefore refuse it, so a DAO plans a correct full scan.
  Carrying the flag changes a Raft command format that existing logs replay
  through, which is its own change. Only an explicit `case_insensitive` tag is
  affected: `index.ParseIndexTags` defaults to case-sensitive, so ordinary
  `index:"true"` and `index:"unique"` fields keep their real engine index.
- **The SDK transport adapter refuses an OPEN range bound** (tracked as #36).
  `transport.IndexClient.IndexScan` takes plain strings, so there is no way to
  express "unbounded": a nil bound arrived as `""`, which every index reads as
  an explicit empty bound and REST rejects as a missing parameter. Since the
  planner leaves one side nil for every one-sided range, `Team >= "beta"`
  became `["beta", "")` and returned empty with a nil error. `ScanIndex` now
  refuses a nil bound so the caller sees the incapability rather than a wrong
  answer.
- **Numerically ordered fields are no longer advertised to the query
  planner** (#35 restores them properly). Index terms are decimal text
  compared lexicographically, so `"10"` sorts before `"2"` and `Age >= 2`
  returned an INCOMPLETE result — worse than empty, because a partial answer
  looks like a real one. This change is what makes those index paths live, so
  leaving the ordering enabled would have converted a uniformly-empty answer
  into a silently-partial one.
  `buildCatalog` therefore omits numeric single-field indexes and compound
  indexes with a numeric member, and the planner chooses a correct full scan.
  The cost is the sound numeric EQUALITY lookup losing its index — slower, not
  wrong — and the direct `QueryByIndex` path is unaffected. The surgical fix
  (the planner refusing only RANGE plans on such fields, using the `FieldType`
  it already carries) changes `pkg/planner`'s documented contract, which is an
  owner decision; #35 carries it with the analysis.
- **Barrier-encrypted tenant stores still cannot index.**
  `seal.tenantSharedKVStore` and `seal.tenantBarrierKVStore` hold only
  ciphertext, so nothing beneath them can read a field value. Indexing above the
  barrier would put the indexed value into an index KEY in the shared store, in
  the clear, defeating the isolation those types exist to provide; a
  keyed/deterministic index term preserves it for exact match but destroys the
  ordering range scans need. That is a cryptographic design decision and is not
  made here. Until it is ruled on these stores refuse, and a DAO over them
  answers correctly by full scan.

## [0.7.0-alpha] - 2026-08-19

MINOR rather than PATCH: no qrdb API moves here, but `go-truststrap` advances a
MINOR of its own and reaches consumers through this module's requirement graph,
so anything composing both takes the change transitively.

### Changed

- **`go-truststrap` v0.1.4-alpha to v0.2.0-alpha.** It splits `pkg/noiseproto`
  on a `fips` build tag — `dh.go` (`//go:build !fips`) performs X25519,
  `dh_fips.go` (`//go:build fips`) refuses with `ErrFIPSUnsupported`. That tag
  is the mechanism by which the Noise finding below gets closed. It is not
  closed here: this module's FIPS job does not pass `-tags fips`, so it still
  compiles the `!fips` variant.

### Fixed

- **`make test` sets an explicit `-timeout=30m`.** It previously set none and
  inherited `go test`'s 10m per-package default, which `pkg/seal` had been
  living inside by a margin of ten seconds: `589.797s` on the last passing run
  against a 600s budget — 98.3% — and then `600.442s` on the next one, which
  failed. Nothing about the package changed between those two runs; it is slow
  by design (Argon2id under `-race`) and the budget was always too small for it.
  The failure shape is the reason this mattered more than the delay: the timeout
  kills the whole test binary and names whichever test was starting when the
  alarm fired, so an exhausted budget reads as a hang in an unrelated package.
  Bounded rather than removed, so a genuine deadlock still fails; the CI job
  already allows 90 minutes and `test/integration` keeps its own `-timeout=25m`,
  which still applies because a later `-timeout` wins.

### FIPS 140-3 posture

Measured on this commit — Go 1.26.6, `GOFIPS140=latest GODEBUG=fips140=only`,
`-race`, and the same build tags `make test` uses:

    122 packages: 106 pass, 16 fail
    693 GCM rejections across 15 packages
     23 X25519 rejections across 3 packages

**This bump moves no package from red to green.** The pass/fail split and the
GCM count are identical to the pre-bump measurement recorded in `ci.yml`, and
the count is stated here so the next person does not have to re-derive it to
find that out. Both remaining findings originate outside this module:

- **GCM with caller-supplied IVs** — every rejection originates in
  `go-quicraft@v0.6.2-alpha/pkg/seal` (`algorithm.go`, `strategy_software.go`).
  qrdb's own five GCM construction sites already use
  `cipher.NewGCMWithRandomNonce`, so there is no qrdb-side residue. Fixed
  upstream in `go-quicraft@v0.7.0-alpha`; taking that bump is gated on the
  wrapped-root-key dual-read migration (go-qrdb#23), because the new
  construction reports `NonceSize() == 0` and an existing sealed store would
  stop unsealing.
- **X25519 in the Noise handshake** — `pkg/clusterenroll` (7), `pkg/ceremony`
  (8) and `cmd/qrdbd` (8). Noise_NK has no approved DH to move to, so this
  never closes by moving to a newer version; it closes by Noise being absent
  from a FIPS build. `pkg/ceremony` is newly counted here: it previously failed
  on GCM before reaching the Noise path.

Neither finding is closable by editing the FIPS job, and it stays out of
`ci-gate` while they stand. Nothing in this repository claims the module
operates under `fips140=only`.

## [0.6.0-alpha] - 2026-08-18

MINOR, not PATCH: pre-1.0 this is where breakage lives, and this release moves
the derivation of persisted role and custodian identifiers, tightens the
multicast shared-secret contract, and removes `statemachine.CachedDiskBackend`.

Two housekeeping defects are closed here rather than left to rot. `VERSION` had
read `0.5.7-alpha` through both the `v0.5.8-alpha` and `v0.5.9-alpha` tags — the
unreconciled-sources-of-truth failure `release-versioning.md` §2 warns about —
and neither of those tags got a CHANGELOG section, so their content was still
sitting under `[Unreleased]`. It is folded into this entry. `v0.5.9-alpha` in
particular carried the `CachedDiskBackend` removal, a breaking change published
as a PATCH; consumers that pinned back to `v0.5.8-alpha` over it should move
forward to this release.

**Consumers on `v0.5.9-alpha` or earlier should upgrade.** The UUIDv8 migration
below is what stops `pkg/role` panicking in package init under
`GODEBUG=fips140=only`; it landed after `v0.5.9-alpha` was cut, so every tagged
release before this one still takes down any downstream binary that authorizes
through `pkg/role` in a FIPS build.

### Added

- **Protected resolution targets for `ConfigProcessor` (`pkg/secret`).** A
  config field declared as `secret.SecretValue` (or `*secret.SecretValue`)
  instead of `string` is now resolved in place inside its own memguard
  enclave, so a resolved secret reaches the consumer without ever resting in
  an unwipeable Go string. `SecretValue` gained YAML/JSON codecs that decode
  from — and re-encode to — the plain reference string, so config documents
  are unchanged and a re-serialized config emits the reference rather than
  the plaintext (a non-scalar node — and JSON `null`, which is a configuration
  mistake rather than an empty secret — fails with the new
  `SecretValueDecodeError`). `Raw` is left intact by the walk.

  The protected target is deliberately stricter than a plain `string` field:
  every non-nil target the walk reaches is either resolved or reported in
  `Unresolved`, never skipped in silence. A target holding nothing (a null or
  absent key), an inline `literal://` secret, a bare value, or a scheme no
  resolver claims is refused with the new `UnresolvableTargetError` — an
  inline secret carries the plaintext in `Raw` itself, an ordinary Go string
  that outlives the walk, so sealing it would leave a second unprotected copy
  and break the guarantee the field type advertises. A target the walk cannot
  write to (boxed by value in an unsettable interface) is refused with the new
  `UnwritableTargetError`; boxed targets it can write to are resolved through
  an addressable copy and assigned back. An optional secret is spelled as a
  `*SecretValue`, where an absent or null key leaves the pointer nil — the one
  case the walk passes over. That is also what closes the YAML null gap:
  `yaml.v3` zeroes a null destination before the unmarshaler is consulted, so
  `password:` and `password: null` never reach `UnmarshalYAML` and are refused
  at the walk instead of at decode time, where JSON rejects its `null` token.

  A protected-target failure fails the call: `Process` returns
  `ErrUnresolvedSecretURI` whenever the new `ProcessResult.ProtectedUnresolved`
  is non-empty, with or without `WithFailClosed()`, because choosing the type
  is itself the request to fail closed on that field. `Process` also destroys a
  target's existing enclave before refilling it, so a failed refresh over an
  already-resolved config leaves the field empty rather than letting a revoked
  credential keep authenticating while its backend is down.

  Additive throughout: `Process`'s signature is unchanged, the deprecated
  string path keeps its exact behaviour — including that its failures stay
  opt-in diagnostics under `WithFailClosed()` — and no existing consumer
  breaks on upgrade.
- **Redacted display paths for `SecretValue` (`pkg/secret`).** `SecretValue`
  now implements `fmt.Formatter` and `slog.LogValuer`, which `fmt` and
  `log/slog` consult ahead of `fmt.Stringer`. `%v`, `%s`, `%+v`, `fmt.Sprint`
  and structured logging of a config struct holding a resolved secret therefore
  render the redacted description instead of the plaintext. New `Redacted()`
  exposes the same description directly: it shows the reference only when a
  resolver established that the reference points at a secret rather than
  carrying one, judging by provenance rather than syntax — the registry falls
  back to `StrategyLiteral` for anything no resolver claims, so a
  reference-shaped `https://user:pw@host` or `unknown://pw` is the secret in
  fact and is redacted, as is any value not yet resolved. Only an explicit
  `String()`/`Bytes()` call still materializes plaintext, and only
  serialization still emits `Raw` verbatim — reproducing the configuration
  document is what serializing it means.
- **`WithNoPlaintextStrings()` migration ratchet (`pkg/secret`).** With this
  processor option set, a plain `string` field holding a reference is left
  untouched — its secret is never fetched — and its path is reported in the
  new `ProcessResult.PlaintextSkipped` (always a subset of `Unresolved`). A
  consumer turns it on, converts the reported fields to `SecretValue` one at
  a time, and adds `WithFailClosed()` once the list is empty to refuse any
  boot that would produce a plaintext string. Both re-exported from
  `sdk/go/secret`.
- **Raft state machines over any storage backend (run-modes phase 5).**
  New `raft.sm_storage` (`--sm-storage`) selects the storage backend the
  storage-generic state machines (`sm_type: disk`/`fast`) compose:
  `pebble` (default), `file`, or `memory`, rooted per-(shard, replica)
  under the data dir. Objstore is refused as the hot apply path with a
  pointed error — it is a snapshot/backup tier. The pebble-native SMs
  (`pebble`, `mvcc`) reject a non-pebble selection.
- **File storage backend durability.** `pkg/storage/file` writes are now
  crash-safe: Set is temp-file + fsync + atomic rename + directory fsync;
  Delete fsyncs the parent; batch commits are all-or-nothing via a
  CRC-verified commit journal published before any per-key file is
  touched and replayed at open (a corrupt journal fails the open rather
  than serving a half-applied batch). New optional `storage.Syncer` seam.
  Acceptance: raft single-node survives a hard kill + restart over
  `--sm disk --sm-storage file`, and a 3-node cluster replicates over
  file-backed SM storage (test/integration/sm_storage_file_test.go).

- **Run-modes requirements ratified** ([docs/run-modes.md](docs/run-modes.md)):
  standalone = no Raft (standard database operations + always-on security
  core over any non-raft storage backend); raft (single- or multi-node) =
  MVCC/watch/lease/lock/transactions/etcd/cluster ops. Feature availability
  follows a three-gate model (build tag > run mode > config) with an
  explicit always-compiled CORE. The daemon's `mode: standalone` and the
  capabilities upgrade land in subsequent phases of the approved plan.

- **Feature profile system (run-modes phase 2).** `pkg/capabilities` now
  seals an effective per-deployment profile at boot
  (`capabilities.Seal(cfg.EffectiveCapabilities())`): the intersection of
  build tags, run mode, and config. `Capabilities` gains `mode` plus
  `lock`/`cluster`/`backup`/`rebalance`; the proto, REST/gRPC/QUIC/MCP/
  embedded transports, SDK DTO, and `qrdb admin capabilities` all carry
  the new fields (additive wire shapes). Before a composition root seals,
  `Get` keeps the historical compile-time fallback.
- **`mode` config key** (`"standalone"`/`"raft"`, empty = raft) with
  validation: under standalone, explicitly enabling `mvcc`/`watch`/
  `lease`, `raft.join`, or a multi-member `raft.peers` list fails with
  `requires mode: raft`; standalone skips the raft identity requirements
  (only `raft.data_dir` stays mandatory). New optional `enabled` toggles
  on `mvcc`/`watch`/`lease` narrow the raft profile.
- **Boot-time build gate** `Config.ValidateCapabilities()`: a wire
  protocol enabled in config but not compiled in is refused with an error
  naming the build tag. (Between phases 2 and 3 the daemon refused
  `mode: standalone` with a pointed error; phase 3 replaced the refusal
  with the real composition branch.)
- **Typed refusal hints.** REST `ServiceNotImplementedError` and the
  gRPC/QUIC/service `ServiceNotConfiguredError` gained an optional `Hint`
  appended to the message (e.g. `requires mode: raft`).

- **`mode: standalone` in the daemon (run-modes phase 3).** `qrdbd` now
  composes the raft-free data plane (`lifecycle.NewLocalStorage`/
  `NewLocalEngine` → `backend.LocalBackend`) when `mode: standalone` (or
  `--mode standalone`) is set: no raft host, no WAL, no election — boot
  drops from seconds to milliseconds. Standard database operations plus
  the always-on security core (mTLS/RBAC/IAM, seal barrier incl. encfs,
  audit) serve as in raft mode; raft-only endpoint families (MVCC
  revisions, watch, lease, lock, etcd-style Txn, cluster/backup/
  rebalance, encryption migration) are refused on every transport with
  501/Unimplemented naming the remedy `requires mode: raft`. The SQL
  catalog keeps its internal MVCC/transaction plumbing (standard ops).
- **One canonical local composition.** The three duplicate
  storage/engine composition sites (SDK node, embedded transport, and
  now the daemon) collapse into `lifecycle.NewLocalStorage`/
  `NewLocalEngine`; the embedded `database/sql` driver gains objstore
  support as a side effect.
- **SDK standalone gating (behavior change, as announced below).**
  `NewStandaloneNode` no longer exposes the MVCC/watch/lease/transaction/
  lock service families; standalone nodes report `mode: standalone` in
  capabilities with those features false. `NewClusteredNode` is
  unchanged. `service.AdminService` gains `WithCapabilityProfile` /
  `lifecycle.Infra.Profile` so each node in a multi-node process reports
  its own truth.
- REST's `capabilitiesResponse` now carries the full profile (a phase-2
  funnel gap), and REST maps `ServiceNotImplementedError` to 501 from
  registry-resolved paths (previously 500).

- **Per-mode exemplars and integration suites (run-modes phase 4).**
  `configs/qrdb-standalone-{pebble,memory,file}.yaml` exemplars;
  `configs/qrdb-standalone-mvcc.yaml` renamed to
  `qrdb-raft-single-mvcc.yaml` (it always was a single-node RAFT config —
  the old name contradicted the ratified terminology).
  `TestStandaloneMode_StandardOpsAndRefusals_PerProtocol` drives a
  `--mode standalone` daemon over all five transports: standard ops
  green, capability truthfulness, raft-only refusals; plus per-backend
  boot coverage. The unix SDK transport's capabilities mapping gained the
  new profile fields (the last funnel site). Downstream notified:
  go-quicbaas#4.

- **Release tag lockstep check.** `make check-tag-lockstep`
  (`scripts/check-tag-lockstep.sh`) checks release-versioning.md P2: every
  root `vX.Y.Z` tag must have an `sdk/go/vX.Y.Z` tag at the same commit, and
  no `sdk/go` tag may exist without its root. The new `Release Lockstep`
  workflow runs it on version-tag pushes in both namespaces (`v*` and
  `sdk/go/v*`) — within a minute of publication, rather than days later in a
  consumer's dependency bump — and re-audits the whole tag set on every merge
  to `main`. It detects a skewed release; it
  cannot prevent one, because a tag is visible the moment it is pushed
  (go-qrdb#13 tracks enforcement). The preventive step is running the check
  before publishing both tags in one atomic push
  (`git push --atomic origin vX.Y.Z sdk/go/vX.Y.Z`). `make test-tag-lockstep` covers
  the rejection branches against throwaway repositories, since this
  repository's own tag set is permanently valid and exercises only the
  accepting branch. `v0.5.9-alpha` was tagged on the root module alone, so
  `go get` at latest resolved the root to `v0.5.9-alpha` and `sdk/go` to
  `v0.5.8-alpha` — a pair that does not compile, because the older `sdk/go`
  still aliased the symbols removed below. The tag has since been cut.
  Reported by go-xkms (#6).

### Deprecated

- **Plaintext-into-`string` secret resolution (`pkg/secret`).** Resolving a
  secret into a plain `string` config field is the plain path the stack is
  moving off; use a `SecretValue` field target, and
  `WithNoPlaintextStrings()` to find the fields that still need converting.
  `SecretValue.String()` and `SecretValue.Bytes()` are likewise deprecated in
  favour of `WithBytes`/`Open` (and `Redacted()` for anything displayed).
  Nothing is removed and the deliberate accessors keep their behaviour: the
  exported signatures stay as they are until the stack's coordinated breaking
  release, so the whole dependency chain can move in one wave rather than repo
  by repo. `String()` still returns plaintext because it is the documented way
  to hand a secret to an API that takes a string, and silently redacting it
  would break those callers at run time with no compile error; the accidental
  paths (`%v`, `fmt.Sprint`, structured logging) are redacted now, as noted
  above, so what remains deferred is only the deliberate call.

### Removed

- **`statemachine.CachedDiskBackend` and `NewCachedDiskBackend`**, with
  their `sdk/go` aliases — a breaking change to an exported API, shipped
  in `v0.5.9-alpha`. Both were a reserved placeholder: a methodless struct
  no registry ever provided, so the `cached-disk` SM backend passed config
  validation and then failed at boot. There is no replacement and nothing
  to migrate — no build that compiled against these symbols was running a
  cached disk backend. The disk-backed path is `sm_type: disk`, now
  composable over any storage backend via `raft.sm_storage`.

### Fixed — SECURITY

- **`GOFIPS140=off` was read as a FIPS request.** SO PIN hashing in
  `pkg/auth` picked its KDF with `os.Getenv("GOFIPS140") != ""`, so the one
  value that explicitly means "not FIPS" selected PBKDF2-SHA256, and
  `crypto/fips140.Enabled()` was never consulted at all. Every
  FIPS-conditional path in the tree now routes through a single predicate,
  `seal.FIPSEnabled` (re-exported from `go-quicraft/pkg/seal`): the Go
  runtime's FIPS 140-3 module state first, `GOFIPS140` as an environmental
  fallback, and `off` honoured as disabled. `pkg/seal/kdf.DefaultHasher`
  loses the same env-only test, and `pkg/pin.AutoDetectHashConfig` — which
  was blind to the environment entirely — now honours a `GOFIPS140` FIPS
  request. Stored hashes are unaffected: each record carries its own
  algorithm/version tag and verification still routes by that tag, so a
  hash written in either mode continues to verify in the other.

- **A resolver reporting success with no value is now refused
  (`pkg/secret`).** `SecretResolver` requires a non-nil `SecretValue` on
  success, but `ResolverRegistry.Resolve` did not enforce it: a resolver
  returning `(nil, nil)` panicked the plain string path on the nil
  dereference, and would have let a protected target be counted as resolved
  while holding nothing. The registry now fails such a resolver with
  `ResolutionFailedError`, so the path is recorded in `Unresolved` and
  `WithFailClosed()` refuses the boot.
- **`ConfigProcessor` no longer destroys a `SecretValue` config field.** The
  walker descended into a `SecretValue`, saw its exported `Raw` reference as
  an ordinary resolvable string field, and overwrote it with the resolved
  plaintext while leaving the enclave empty — making the documented "hold the
  secret in a `SecretValue`" pattern strictly worse than a plain `string`
  field. A `SecretValue` is now a protected leaf the walk resolves into and
  never descends through.

### Changed — protected memory

- **The live cluster DEK is held in mlocked memory, and never copied out.**
  `dek.ClusterDEKManager` kept the plaintext cluster DEK in an ordinary
  `[]byte` for the manager's whole lifetime. It is now a `*secure.Buffer`
  (mlocked, guard-paged, wiped on destroy): the key is generated straight
  into protected memory rather than being copied there, and the superseded
  buffer is destroyed on `Rotate` and on `Close`.

  **BREAKING (service seam):** `ClusterDEKManager.GetCurrentDEK() ([]byte,
  error)` is gone, and no exported API replaces it: the key is now applied
  inside the manager by `WrapKey(plaintext []byte) ([]byte, error)` and
  `UnwrapKey(wrapped []byte) ([]byte, error)`, which perform the AES-256-GCM
  key wrapping under the manager's state lock. Neither a heap-backed copy nor
  a slice aliasing protected memory crosses the API boundary, so no caller
  can retain the key past a call or mutate the live key.
  `ClusterDEKManager.CurrentVersion()` is the checked
  counterpart of `GetDEKVersion` for callers that must distinguish
  "version 0" from "no DEK". Correspondingly,
  `service.DEKServicer.GetCurrentDEK(ctx) ([]byte, uint64, error)` becomes
  `CurrentDEKVersion(ctx) (uint64, error)`: the DEK bytes never crossed that
  boundary usefully — the embedded transport already discarded them, and no
  transport exposes DEK material.

### Changed — FIPS 140-3 identifiers

- **Deterministic role and custodian identifiers moved from UUIDv5 (SHA-1) to
  RFC 9562 §5.8 UUIDv8 over SHA-256.** `crypto/sha1` panics under
  `GODEBUG=fips140=only`, and both derivations run during package
  initialization and tenant seeding, so before this change a FIPS build died
  before a single test body ran — including in any downstream binary that
  authorizes through `pkg/role`. UUIDv8 is the version reserved for
  application-defined derivations, and it is the only one available: RFC 4122
  fixes v3 to MD5 and v5 to SHA-1, so no versioned name-based UUID can carry an
  approved hash. Inputs are length-prefixed before hashing so the encoding is
  injective — unframed, tenant `a/b` with role `c` and tenant `a` with role
  `b/c` would collide on one identifier. The 36-character UUID text shape is
  unchanged, so no index key or wire format reformats.

  **These identifiers are persisted data**, so the change is versioned rather
  than a flag day: writes emit only the v8 form and reads accept both. A
  cluster seeded by an older build keeps its v5 identifiers, and code holding a
  tenant plus a role NAME must not re-derive — on such a cluster the v8 value
  names no record at all. Use `role.ResolveSystemRoleID`, which answers from
  the store's name index and falls back to the canonical value, and
  `role.SystemRoleIDs`, which returns every identifier denoting the role so
  that the SO deny-list and the separation-of-duties Owner strip still match a
  grant made before the migration. Writing a canonical identifier straight into
  a legacy cluster's user records produces a grant that resolves to no role and
  silently strips the user's permissions.

### Fixed — release integrity

- **Every previously published tag recorded an unresolvable requirement for its
  own SDK submodule.** The root `go.mod` recorded
  `github.com/jeremyhahn/go-qrdb/sdk/go v0.0.0-00010101000000-000000000000`, a
  placeholder that resolves to nothing. A `replace` directive only applies in
  the main module, so the in-repo `=> ./sdk/go` masked it for every check run
  inside this repository — local builds, CI, `go list`, module-zip inspection —
  while a downstream consumer got:

  ```
  github.com/jeremyhahn/go-qrdb/sdk/go@v0.0.0-00010101000000-000000000000:
      invalid version: unknown revision 000000000000
  ```

  `pkg/ceremony`, `pkg/api/rest` and `cmd/qrdb` import `sdk/go/transport`, so
  the failure is reachable rather than theoretical: a scratch module that does
  `go get github.com/jeremyhahn/go-qrdb@v0.5.9-alpha` and imports
  `pkg/ceremony` cannot even `go mod tidy`. Reproduced with `GOWORK=off`.

  **Why nobody hit it.** Every current consumer — go-xkms, go-quicbaas,
  go-trusted-platform — carries its own explicit
  `require github.com/jeremyhahn/go-qrdb/sdk/go` line at a real version,
  because each also imports `sdk/go/transport` directly. Their own requirement
  satisfies the import, and the root module's nonsense version is never
  consulted. The defect therefore bites a NEW consumer that reaches a root
  package without independently depending on the SDK, which is precisely what
  the mandated throwaway-consumer check constructs. Latent, not dormant.

  The reciprocal pin was stale in the same way: `sdk/go` required the root at
  `v0.4.0-alpha`, five minor versions behind.

  Both cross-module requirements now name the release they ship in
  (`v0.6.0-alpha`), which the lockstep tagging rule guarantees exists — the
  root and `sdk/go` tags are pushed atomically. The `replace` directives stay,
  because in-repo development must still build against the working tree rather
  than a published copy of its own code.

  This is the defect `release-versioning.md` §2 mandates a throwaway-consumer
  check for, and the reason that check is mandatory: no test that runs inside
  the repository can see it. It is also the same failure that condemned
  `v0.2.0-alpha`, which means it regressed and went unnoticed for many
  releases.

### Fixed — tests

- **`TestDefaultHasher_NonFIPSMode` pinned the toolchain instead of the
  selector.** It cleared `GOFIPS140` and asserted the literal Argon2id version,
  but `seal.FIPSEnabled` consults `crypto/fips140.Enabled` before falling back
  to the variable, and `GOFIPS140` selects the module at BUILD time — `t.Setenv`
  cannot retract at run time what the linker already decided. The expectation
  now routes through the `wantVersion` helper the package's other FIPS-selection
  tests already use. No coverage is dropped and nothing is skipped: the test
  asserts Argon2id on an ordinary run and PBKDF2-SHA256 under the FIPS job,
  which is the correct answer for a FIPS binary because Argon2id is not an
  approved KDF. This was the last go-qrdb-side failure under
  `GODEBUG=fips140=only`; the remainder are upstream (see
  `docs/seal/fips140-mode.md`).

## [0.5.8-alpha] - 2026-08-17

### Added

- **Live ABAC policy reload.** `pkg/auth/abac.Decorator` now supports
  `Reload(policies []*abac.Policy) error`: the new set is compiled and
  validated before an atomic pointer swap, so a failed reload leaves the
  previous policy set fully in force and `Authorize` reads lock-free under
  concurrent reloads. `abac.WithLiveReload` keeps the decorator reloadable
  even when zero policies are configured at construction. `sdk/go/node.Node`
  exposes the seam via `WithABACLiveReload` / `Node.ABACPolicyReloader()`;
  `pkg/auth/wiring.AuthorizerConfig`/`AuthMiddlewareConfig` gained an
  `Authorizer` override field so the embedded in-process transport and every
  network transport's `AuthMiddleware` share one authorizer instance, and a
  reload reaches all of them identically. `sdk/go/types_abac.go` re-exports
  `ABACPolicyReloader` and `ABACWithLiveReload`.

## [0.5.7-alpha] - 2026-08-17

### Fixed — SECURITY-ADJACENT

- **Module-registered public routes serve anonymously.** The REST
  middleware short-circuited only its hardcoded built-in allowlist before
  authentication; a module's `HandlePublic` routes (WebAuthn
  registration/login ceremonies, OIDC discovery) still demanded a peer
  certificate once `client_auth` was enabled, breaking every anonymous
  first-contact ceremony. Public registry routes now short-circuit
  pre-authentication exactly like the built-ins.

## [0.5.6-alpha] - 2026-08-17

### Added

- **Pre-resolved scope permissions** (`auth.Identity.ScopePermissions`):
  a TRUSTED in-process issuance path (a module authentication fallback
  validating a platform token) may carry the token's `scp`-derived
  `resource:action` set on the identity. The RBAC authorizer unions them
  with store-resolved role permissions — additive only, SO deny-list
  still overrides — so client-credential/app tokens whose subject has no
  user record authorize for exactly what the token was issued for. Never
  wire-deserialized; the on-behalf path constructs fresh identities and
  cannot carry them.

## [0.5.5-alpha] - 2026-08-17

### Changed

- **Built-in routes yield to module registrations.** The documented
  "modules register first and take precedence" contract is now formal:
  a built-in route whose method+pattern a capability module already
  claimed is skipped (debug-logged) instead of colliding. Module-vs-
  module duplicates still fail construction loudly.
- qrdbd auth bootstrap no longer opens an unchained `audit.log` side
  file (the decision bridge made it obsolete); a nonexistent data dir no
  longer fails auth bootstrap.

## [0.5.4-alpha] - 2026-08-16

### Added

- SDK audit-record aliases (`qrdbsdk.NodeAuditEvent` / `NodeAuditFilter` /
  `NodeAuditPage`): composing hosts emit into the node's chained audit
  sink through the SDK boundary without importing `pkg/audit`.

## [0.5.3-alpha] - 2026-08-16

### Added

- **Module authentication fallback** (`apimodule.ModuleAuthFallback`, SDK
  `qrdbsdk.ModuleAuthFallback`): a capability module whose HTTP surface
  accepts credentials qrdb's own authenticators do not understand (a
  session-token plane) may authenticate the request when the host chain
  produces no identity. The fallback identity flows through the SAME
  store-resolved enrichment and the ONE authorizer as an mTLS identity.
- **REST route-collision guard**: registering the same method+pattern
  twice (two modules, or a module and the built-in surface) fails
  construction with a descriptive panic instead of silently shadowing.

### Changed — one audit plane

- qrdbd's per-decision auth audit now rides the tamper-evident chained
  event log via the decision bridge — identical wiring to embedded nodes;
  the unchained side file is gone.
- The legacy second audit system (`audit.New`, kv/file/multi backends,
  the `audit:` config block, `DaemonAuditConfig`) is DELETED. The chained
  `AuditLogger` (Emit/Query) is the only audit sink; the `Logger`/`Entry`
  middleware contract remains as the decision-bridge interface.
- Audit wire tags are snake_case (`tenant_id`, `client_ip`,
  `request_id`, `prev_hash`), closing the documented cross-project
  serialization mismatch.

## [0.5.2-alpha] - 2026-08-16

### Changed — SECURITY

- **Authentication is now the zero value.** `AuthConfig.Enabled` became a
  pointer (`*bool`) with `IsEnabled()`: an embedder that builds a
  `DaemonConfig` literal without touching the Auth block gets an
  AUTHENTICATED unified server; only an explicit `false` disables. This
  closes a live regression where both downstream daemons (xkmsd,
  quicbaasd) served every qrdb-native plane unauthenticated because the
  plain-bool zero value silently disabled the middleware. Use
  `config.BoolPtr` / `qrdbsdk.BoolPtr` for explicit values.

### Added

- **Per-decision audit on embedded nodes** (`audit.NewDecisionLogger`):
  the auth middleware's decision trail now bridges onto the node's
  chained compliance event log — authorization decisions land in the
  same tamper-evident chain as every other audited operation, matching
  qrdbd. Closes the documented SDK-node TODO.

## [0.5.1-alpha] - 2026-08-16

### Fixed

- **SDK error exports restored**: `sdk/go/types_errors.go` was accidentally
  truncated in 0.5.0-alpha to only the new `NotLeaderError` re-export,
  dropping `QRDBError`, `ErrorCode`, the error constructors and every
  other error alias. The full surface is restored with `NotLeaderError`
  appended. 0.5.0-alpha should not be depended upon.

## [0.5.0-alpha] - 2026-08-16

### Added

- **ABAC authorization decorator** (`pkg/auth/abac`): attribute-based
  access control layered over the RBAC authorizer, porting the core of
  go-quicbaas's IAM policy engine (YAML policy schema, compiler, resource
  trie, condition parser, evaluator) into its canonical qrdb home. Hybrid
  semantics: explicit deny overrides RBAC allow, explicit allow grants
  beyond RBAC, unmatched requests delegate to RBAC. Policies are
  tenant-scoped; the reserved system tenant's policies apply platform-wide
  as guardrails. Enabled in qrdbd via `auth.abac.{enabled,policy_dir}`
  (fail-closed validation and boot) and in embedded nodes via
  `node.WithABACPolicyDir` / `node.WithABACPolicies`. SDK re-exports in
  `sdk/go/types_abac.go` (`ABACPolicy`, `DecorateAuthorizer`, ...) plus
  `qrdbsdk.DaemonAuthABACConfig`. Policies load once at construction;
  hot-reload is a documented follow-up. See `docs/auth/abac.md`.

- **Embedded-node auth enforcement** (`pkg/auth/wiring`): the SDK node
  (`NewStandaloneNode`/`NewClusteredNode`) now constructs the SAME auth
  middleware qrdbd builds — RBAC authorizer (+ABAC decoration), mTLS/SPKI
  authenticator, adaptive/enforced mode — and passes it to the unified
  multi-protocol server. Previously an embedded node served every
  permission-gated route unauthenticated. `BuildAuthMiddleware` /
  `BuildAuthorizer` are shared by `cmd/qrdbd` and the SDK node (one
  construction path, no divergence).

- **Per-RPC public affordance for module gRPC services**
  (`apimodule.GRPCPublicMethodLister`, re-exported as
  `qrdbsdk.ModuleGRPCPublicMethodLister`): modules may declare exact gRPC
  methods that bypass authentication, symmetric with REST's
  `HandlePublic`. Registered method names are validated at startup
  (typed error on malformed names). Built-in health/reflection exemptions
  now genuinely bypass authentication rather than only the permission
  lookup.

- **Module MCP tool permissions** (`apimodule.MCPToolPermissioner`): the
  unified MCP server consumes per-tool permission declarations from
  registered modules.

### Fixed

- **Sealed store crash durability (data-loss grade)**: the seal store —
  the only persisted copy of the barrier root key and epoch state — was
  opened with `SyncWrites: false` (a rationale valid only for
  Raft-replicated stores). A crash could lose the root-key write; the
  next boot silently minted a fresh root key, permanently orphaning every
  file encrypted under the lost key (`quicraft/seal: decryption failed`
  on the data-plane and raft snapshot stores). All seal/DEK stores now
  force `SyncWrites: true`; SIGKILL-subprocess regression tests prove a
  killed node reboots and serves its data (`pkg/seal/encfs`,
  `sdk/go/node`).

## [0.4.0-alpha] - 2026-08-15

### Added

- **ServiceModule MCP tool authorization** (`apimodule.MCPToolPermissioner`):
  a module that registers MCP tools may now declare each tool's required
  permission; the unified server feeds the declarations into the same MCP
  tool registry the built-in tools use, so module tool calls are authorized
  by the one middleware path instead of being unconditionally denied. A
  module tool registered without a declaration remains fail-closed.
- **SDK re-exports for module hosts**: `qrdbsdk.WithServerTLS` (the
  TLS-only transports' certificate source, required by hosts that terminate
  TLS with their own material), `qrdbsdk.NewModuleHTTPRegistrar` (capture
  registrar for module tests), `qrdbsdk.ModuleMCPToolPermissioner`, and
  `qrdbsdk.PermissionIdentityGated` (the identity-gated sentinel).

### Changed

- `github.com/jeremyhahn/go-quicraft` pinned to `v0.4.2-alpha`
  (`batch.EncodeBatch` returns `([]byte, error)`; the error is propagated
  through `pkg/batch`).

### Fixed

- `pkg/api/mcp` default-tag test builds: the shared `CredentialServicer`
  mock lived in a file constrained to the full feature-tag set while an
  untagged test file used it; the mock now lives untagged so every tag
  combination compiles.

## [0.3.0-alpha] - 2026-08-14

### Added — the unified API server seam

Phases 0-2 and phase 3 wave 1 of
`software/plans/unified-api-server-2026-08-14.md`, the work that lets a host
process expose go-qrdb's own control plane instead of reimplementing it per
transport. New exported API, so MINOR.

- `pkg/apimodule`: `ServiceModule` and `BaseModule` with `Start`/`Stop`, a
  duplicate-name check, and a startup assertion that every registered route
  carries a permission. `WithModule` fans a module out across the transports.
  Re-exported from `sdk/go`. Additive — nothing existing changes shape.
- The control plane's own admin, rebalance and user-auth-factor surfaces are
  served through the seam.
- `pkg/sql/privilege`: GRANT, REVOKE and SHOW GRANTS parsing, rewriting and
  enforcement over `service.PrivilegeService`, plus `pkg/sql/pgcatalog`.

### Changed

- REST runs on chi/v5 v5.3.0. SSE `/watch`, on-behalf-of and the cluster-peer
  exempt routes are unchanged, gated by the pre-existing watch and
  barrier-authz tests passing unmodified.
- One authorization path: `authz.proto` carries `option (auth.permission)`, a
  reflection interceptor reads it, and `Perm_*` constants replace the five
  hand-maintained permission tables. Default-deny, the `IdentityGated`
  sentinel, the public-route allowlists and on-behalf-of handling are retained.
  QUIC and MCP gain method registries with mapping guard tests and REST a route
  guard, so a handler added without a permission now fails a test instead of
  shipping unauthenticated.

### Removed

- `pkg/auth/middleware/mapping.go` and the five permission tables it fed.
- The `pkg/sql/internal/privprobe` scaffolding, superseded by `pkg/sql/privilege`.

## [0.2.1-alpha] - 2026-08-14

### Fixed

- The two in-repo modules pinned each other at `v0.1.0-alpha`, so a consumer
  taking `sdk/go v0.2.0-alpha` resolved the parent module to the release that
  still had Avro, and the build failed with `undefined: codec.Avro` — the new
  SDK pulls go-codec v1.4.0, which the old parent's `pkg/dao` cannot compile
  against. Both modules carry a local-path `replace` at each other, which
  applies only in a main module, so every in-repo build resolved through the
  working tree and nothing surfaced the stale requirement. `sdk/go` now
  requires the parent at `v0.2.1-alpha` and the root requires `sdk/go` at the
  same version.

`v0.2.0-alpha` and `sdk/go/v0.2.0-alpha` are superseded and must not be
pinned. The tags remain, per the immutability rule.

## [0.2.0-alpha] - 2026-08-14

### Removed — BREAKING: the Avro codec is gone from both modules

`github.com/hamba/avro` is archived upstream and carries reachable advisories
GO-2026-5046, GO-2026-5047 and GO-2026-5048 with no fix forthcoming and no
production-grade replacement, so Avro leaves the product stack entirely (owner
decision, 2026-08-14; the same decision produced go-codec v1.4.0). These
exported identifiers no longer exist:

- `pkg/codec`: `TypeAvro`, `AvroCodec`
- `pkg/dao`: `CodecAvro`

The `codec_avro` build tag and the `WITH_CODEC_AVRO` Makefile switch are gone
with them, as are the `.avro` and `.avsc` entries in extension-based format
detection and Avro's membership in `probeableCodecs`. A caller that selected
Avro must choose another codec; the JSON, YAML, TOML, MsgPack, BSON, CBOR and
ProtoBuf codecs are unaffected.

### Added

- `cmd/qrdb` and `cmd/qrdbd` sources, and the Wails frontend, are actually in
  the repository. They had never been committed: the root `.gitignore` carried
  unanchored `qrdb` and `qrdbd` patterns intended for the compiled binaries,
  and a gitignore pattern without a slash matches at any depth and matches
  directories, so both command trees — 222 Go files — were excluded. `HEAD` did
  not build.
- `workflow_dispatch` trigger on the CI workflow (ci-template v1.3), so a run
  lost to a runner crash or host overload can be retried without a commit.

### Changed

- `github.com/jeremyhahn/go-codec` pinned to `v1.4.0`, the Avro-free release.
- `github.com/jeremyhahn/go-quicraft` pinned to `v0.3.1-alpha`.
- `golang.org/x/crypto` to `v0.55.0` and `golang.org/x/text` to `v0.41.0` in
  the `sdk/go/gorm/conformance` module, clearing ten HIGH advisories
  (CVE-2026-39828 through CVE-2026-46597, and CVE-2026-56852).

### Fixed

- HTTP/3 listener errors were compared against `http.ErrServerClosed` with
  `!=`, and the pgsql row collector compared against `io.EOF` with `==`; a
  wrapped sentinel from either was mishandled. Both now use `errors.Is`.
- The eight registry mocks in the transport and SQL provider tests implement
  `service.Registry.PrivilegeService`, and the MVCC error stubs are referenced
  by the names they actually have. Without these no test binary in the
  affected packages compiled, which also made `gosec` exit non-zero after
  reporting no findings.
- `TestRunStart_OpenAll_DatabasesDirPermissionError` provoked its failure with
  a 0o000 directory, which root ignores; under CI it started a real node and
  hung until the test binary's deadline. It now uses a regular file where the
  directory belongs, failing with `ENOTDIR` for root and non-root alike.
- `TestNewStartCmd_UnknownSM_NoConfig` read the package directory as its data
  directory, picking up a stray committed `data/ca.pem` and failing before it
  reached the assertion it was written for.

### Security

- `cmd/qrdbd/data/` is ignored. A locally run daemon issues itself a CA, node
  and admin key there, and the directory was previously covered only as
  collateral of the unanchored `qrdbd` pattern.
- Playwright's generated `playwright-report/` and `test-results/` output no
  longer ships in the repository.

[Unreleased]: https://github.com/jeremyhahn/go-qrdb/compare/v0.17.0-alpha...HEAD
[0.17.0-alpha]: https://github.com/jeremyhahn/go-qrdb/compare/v0.16.3-alpha...v0.17.0-alpha
[0.6.0-alpha]: https://github.com/jeremyhahn/go-qrdb/compare/v0.5.9-alpha...v0.6.0-alpha
[0.3.0-alpha]: https://github.com/jeremyhahn/go-qrdb/compare/v0.2.1-alpha...v0.3.0-alpha
[0.2.1-alpha]: https://github.com/jeremyhahn/go-qrdb/compare/v0.2.0-alpha...v0.2.1-alpha
[0.2.0-alpha]: https://github.com/jeremyhahn/go-qrdb/compare/v0.1.1-alpha...v0.2.0-alpha
