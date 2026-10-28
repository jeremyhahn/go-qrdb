# Shamir Custody — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

Every capability below is served on all six transports with the same
semantics and the same error classes. gRPC service `qrdb.v1.QRDBService`
(tenant methods on `qrdb.v1.TenantBarrierService`); the Unix socket transport
serves the same gRPC wire over a UDS, so its methods match the gRPC column.
The embedded column names the `transport.BarrierClient` /
`transport.TenantClient` method the Go SDK calls in-process.

The barrier's own operations are [../barrier-seal/api.md](../barrier-seal/api.md).

## Capability matrix — system barrier share holders

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded | `scp` permission |
|---|---|---|---|---|---|---|
| Enrol holder (unseal domain) | `POST /v1/barrier/officers` | `BarrierOfficerRegister` | `barrier.OfficerRegister` | `qrdb_barrier_officer_register` | `OfficerRegister` | `seal:officer_register` |
| Enrol holder (recovery domain) | `POST /v1/barrier/recovery/officers` | `BarrierRecoveryOfficerRegister` | `barrier.RecoveryOfficerRegister` | `qrdb_barrier_recovery_officer_register` | `RecoveryOfficerRegister` | `seal:officer_register` |
| List holders | `GET /v1/barrier/officers` | `BarrierOfficerList` | `barrier.OfficerList` | `qrdb_barrier_officer_list` | `OfficerList` | `seal:officer_list` |
| Claim begin | `POST /v1/barrier/officers/{username}/claim/begin` | `BarrierShareClaimBegin` | `barrier.ShareClaimBegin` | `qrdb_barrier_share_claim_begin` | `ShareClaimBegin` | **identity-gated** |
| Claim complete | `POST /v1/barrier/officers/{username}/claim/complete` | `BarrierShareClaimComplete` | `barrier.ShareClaimComplete` | `qrdb_barrier_share_claim_complete` | `ShareClaimComplete` | **identity-gated** |
| Recovery claim begin | `POST /v1/barrier/recovery/officers/{username}/claim/begin` | `BarrierRecoveryShareClaimBegin` | `barrier.RecoveryShareClaimBegin` | `qrdb_barrier_recovery_share_claim_begin` | `RecoveryShareClaimBegin` | **identity-gated** |
| Recovery claim complete | `POST /v1/barrier/recovery/officers/{username}/claim/complete` | `BarrierRecoveryShareClaimComplete` | `barrier.RecoveryShareClaimComplete` | `qrdb_barrier_recovery_share_claim_complete` | `RecoveryShareClaimComplete` | **identity-gated** |
| Quorum challenge | `POST /v1/barrier/quorum/begin` | `BarrierQuorumBegin` | `barrier.QuorumBegin` | `qrdb_barrier_quorum_begin` | `BarrierQuorumBegin` | **identity-gated** |
| Quorum submission | `POST /v1/barrier/quorum/submit` | `BarrierQuorumSubmit` | `barrier.QuorumSubmit` | `qrdb_barrier_quorum_submit` | `BarrierQuorumSubmit` | **identity-gated** |
| Start rekey | `POST /v1/barrier/rekey/start` | `BarrierRekeyStart` | `barrier.RekeyStart` | `qrdb_barrier_rekey_start` | `BarrierRekeyStart` | `seal:rekey` + SO PIN |
| Revoke holder | `POST /v1/barrier/officers/revoke` | `BarrierOfficerRevoke` | `barrier.OfficerRevoke` | `qrdb_barrier_officer_revoke` | `BarrierOfficerRevoke` | `seal:officer_revoke` + SO PIN |

## Capability matrix — distribution

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded | `scp` permission |
|---|---|---|---|---|---|---|
| Initialize Shamir custody | `POST /v1/barrier/shamir/initialize` | `BarrierShamirInitialize` | `barrier.ShamirInitialize` | `qrdb_barrier_shamir_initialize` | `BarrierInitializeShamir` | `seal:initialize_shamir` |
| Generate recovery key | `POST /v1/barrier/recovery/generate` | `BarrierRecoveryGenerate` | `barrier.RecoveryGenerate` | `qrdb_barrier_recovery_generate` | `BarrierGenerateRecoveryKeys` | `seal:generate_recovery` + SO PIN |
| Recovery status | `GET /v1/barrier/recovery/status` | `BarrierRecoveryStatus` | `barrier.RecoveryStatus` | `qrdb_barrier_recovery_status` | `BarrierHasRecoveryKeys` | `seal:status` |
| Delete recovery key | `DELETE /v1/barrier/recovery` | `BarrierRecoveryDelete` | `barrier.RecoveryDelete` | `qrdb_barrier_recovery_delete` | `BarrierDeleteRecoveryKeys` | `seal:generate_recovery` |
| Initialization ceremony with holders | `POST /v1/ceremony/initialize` | `CeremonyInitialize` | `ceremony.Initialize` | `qrdb_ceremony_initialize` | `CeremonyInitialize` | `ceremony:initialize` |

Unseal, seal, root-token, recovery and the completion of a rekey have no
route of their own: each is the quorum submission with its `op`.

On MCP, the SO PIN of `qrdb_barrier_rekey_start`, `qrdb_barrier_officer_revoke`
and `qrdb_barrier_recovery_generate` is `so_pin` as text or `so_pin_b64` as
standard base64, never both, and always a JSON string
([../barrier-seal/api.md](../barrier-seal/api.md#secret-arguments-over-mcp)).
The Go SDK's MCP client always sends `so_pin_b64`.

## Who may register, list and revoke node holders

The node barrier's holder registry decides who holds a share of its key:
every rekey, revocation and recovery-key generation sends a share to every
registered holder. `seal:officer_register` (both the unseal and the recovery
domain), `seal:officer_list`, `seal:officer_revoke`, `ceremony:initialize` and
`ceremony:sign_csr` are therefore **cross-scope**: only an identity of the
`system` tenant may exercise them, on every transport, and a tenant's SO or
Admin is refused with `403` / `PermissionDenied` even though its built-in role
carries the grant. The operations that change the node barrier
(`seal:rekey`, `seal:bootstrap`, `seal:generate_recovery`,
`seal:initialize_shamir`) are cross-scope for the same reason. `seal:status`
is not cross-scope.

A tenant's own holders are registered through `POST /v1/tenants/{id}/barrier/officers`
(`tenant_barrier:officer_register`).

First-boot registration is not affected. On the boot-time sealed-window
listener holder and recovery-holder registration are anonymous while the
enrollment ceremony is pre-operational, and that listener runs no RBAC.

## Capability matrix — tenant barrier share holders

A dedicated tenant barrier has the unseal domain only.

| Capability | REST | gRPC / Unix (`TenantBarrierService`) | QUIC | MCP tool | `scp` permission |
|---|---|---|---|---|---|
| Enrol holder | `POST /v1/tenants/{id}/barrier/officers` | `OfficerRegister` | `tenant.BarrierOfficerRegister` | `qrdb_tenant_barrier_officer_register` | `tenant_barrier:officer_register` |
| List holders | `GET /v1/tenants/{id}/barrier/officers` | `OfficerList` | `tenant.BarrierOfficerList` | `qrdb_tenant_barrier_officer_list` | `tenant_barrier:officer_list` |
| Claim begin | `POST /v1/tenants/{id}/barrier/officers/{username}/claim/begin` | `ShareClaimBegin` | `tenant.BarrierShareClaimBegin` | `qrdb_tenant_barrier_share_claim_begin` | **identity-gated** |
| Claim complete | `POST /v1/tenants/{id}/barrier/officers/{username}/claim/complete` | `ShareClaimComplete` | `tenant.BarrierShareClaimComplete` | `qrdb_tenant_barrier_share_claim_complete` | **identity-gated** |
| Quorum challenge | `POST /v1/tenants/{id}/barrier/quorum/begin` | `QuorumBegin` | `tenant.BarrierQuorumBegin` | `qrdb_tenant_barrier_quorum_begin` | **identity-gated** |
| Quorum submission | `POST /v1/tenants/{id}/barrier/quorum/submit` | `QuorumSubmit` | `tenant.BarrierQuorumSubmit` | `qrdb_tenant_barrier_quorum_submit` | **identity-gated** |
| Start rekey | `POST /v1/tenants/{id}/barrier/rekey/start` | `RekeyStart` | `tenant.BarrierRekeyStart` | `qrdb_tenant_barrier_rekey_start` | `tenant_barrier:rekey` |
| Revoke holder | `POST /v1/tenants/{id}/barrier/officers/revoke` | `OfficerRevoke` | `tenant.BarrierOfficerRevoke` | `qrdb_tenant_barrier_officer_revoke` | `tenant_barrier:officer_revoke` |

## A Shamir tenant barrier init and its pending first admin

A Shamir init of a tenant barrier that names its holders inline is the
two-round init:

| Round | REST | gRPC / Unix (`TenantBarrierService`) | QUIC | MCP tool |
|---|---|---|---|---|
| Begin | `POST /v1/tenants/{id}/barrier/init/begin` | `InitBegin` | `tenant.BarrierInitBegin` | `qrdb_tenant_barrier_init_begin` |
| Complete | `POST /v1/tenants/{id}/barrier/init/complete` | `InitComplete` | `tenant.BarrierInitComplete` | `qrdb_tenant_barrier_init_complete` |

A first admin whose key signs away from the node uses the signed pair instead
([../multi-tenancy/api.md](../multi-tenancy/api.md#signed-barrier-init-a-first-admins-key-that-signs-elsewhere)):

| Round | REST | gRPC / Unix (`TenantBarrierService`) | QUIC | MCP tool |
|---|---|---|---|---|
| Begin | `POST /v1/tenants/{id}/barrier/signed-init/begin` | `SignedInitBegin` | `tenant.BarrierSignedInitBegin` | `qrdb_tenant_barrier_signed_init_begin` |
| Complete | `POST /v1/tenants/{id}/barrier/signed-init/complete` | `SignedInitComplete` | `tenant.BarrierSignedInitComplete` | `qrdb_tenant_barrier_signed_init_complete` |

A Shamir signed begin takes the same `officers` and `policy` roster, validates
it and prepares the split before it returns the statement, and answers with
`threshold`, `total_shares`, `share_count` and the issued `holders`; its
complete answers like `InitComplete`, `admin_pending` included.
`qrdb tenant barrier-init-begin` takes `--officers`, `--custodians` and
`--policy` as `barrier-init` does.

The completion answers with the holder certificates the init issued, and with
`admin_pending` on every transport: the JSON field `admin_pending` on REST,
QUIC and MCP, omitted when false, and field 10, `admin_pending`, of
`TenantBarrierInitResponse` on gRPC and Unix. When it is set,
`qrdb tenant barrier-init` prints an `AdminPending:` line, and under
`--output json` it carries `admin_pending: true`.

The init unseals only the node that serves it. The tenant's pending first
admin is created at the leader of the tenant's home group. The init tries
once, and if this node cannot complete the admin yet, the init still
succeeds: it returns the holder certificates with `admin_pending: true`, and
the pending admin record stays. The init never waits for the home group,
because that group may need the other nodes unsealed before it has a leader,
and only these holders can unseal them. Any other failure to complete the
admin fails the init; the barrier stays initialized.

When `admin_pending` is set, the holders claim their shares and quorum-unseal
the other nodes. Each unseal that completes a quorum completes the admin if
that node leads the home group, and so does a barrier status read on a node
where the barrier is unsealed. Either can block for up to the admin's 2-minute
completion budget on a node that does not lead the group. Until the admin is
complete, its certificate names no account.

## Why claims and submissions are identity-gated

Holders use these routes while the barrier is sealed, and a holder identity is
not a login identity. The middleware therefore requires only an authenticated
caller, and **the handler enforces the real authorization**: for a claim, that
the connection presents the holder's enrolled certificate and the nonce is
signed by its key; for a submission, that the factor proof verifies against
the roster the key is bound to. The routes are reachable through the sealed
window ([../seal/sealed-window.md](../seal/sealed-window.md)).

A claim over a network transport **requires** a client certificate on the
connection. On the embedded transport, which has no connection, the claim is
bound to the holder's enrolled certificate (`ceremony.WithInProcessClaim`) and
the nonce signature by its key is the proof. A quorum submission does not — the factor proof authenticates the holder — but when a
certificate is present the `mtls` factor also requires it to be the holder's.
The peer certificate is always taken from the connection, never from the
request.

## Schemas

Enrolment request (JSON names; the protobuf fields match):

```json
{
  "username": "carol",
  "role": "custodian",
  "csr_pem": "-----BEGIN CERTIFICATE REQUEST-----...",
  "encryption_kem": "mlkem768-x25519",
  "encryption_key": "<base64 public key>",
  "factors": [{ "name": "fido2", "params": { "...": "..." } }],
  "designated": "fido2"
}
```

`role` is `so` (default) or `custodian`. `encryption_kem`/`encryption_key` are
optional; without them the envelope is sealed to the certificate key (P-256 or
P-384). `factors` enrols factors beyond the built-in `mtls`; `designated`
defaults to `mtls`. The response carries the CA-issued `cert_pem`, the `role`,
the `designated` factor, the `encryption_kem` and the enrolled `factors` —
never a share.

Holder status (list): `username`, `role`, `cert_thumbprint`, `share_index`,
`designated`, `factors`, `encryption_kem`, `enrolled_at`, `claimed_at`,
`share_pending`. Never a share.

Claim begin response: `{"nonce_hex": "...", "expires_at": "..."}` — a
5-minute nonce. Claim complete request: `{"nonce_hex": "...", "signature":
"<base64 signature over the share-claim message>"}`. Claim complete response:
`{"share_index": 3, "envelope": "<base64>"}` — exactly once.

The **share-claim message** is the following, NUL-separated:

```
qrdb/share-claim/v1 \0 <claim label> \0 <username> \0 <nonce bytes>
```

The claim label names the barrier:

| Barrier | Claim label |
|---|---|
| System, unseal domain | `system` |
| System, recovery domain | `recovery` |
| Tenant barrier | `tenant:<tenant id>` |
| Database barrier | `database:<database name>` |

The signature format is that of the `mtls` factor: ECDSA (ASN.1 DER) over
SHA-256 of the message, or Ed25519 over the message (a holder's key is ECDSA
P-256, P-384, P-521 or Ed25519; RSA is refused at enrolment). The `mtls`
quorum factor signs its own domain-separated challenge message
(`mtls.ChallengeMessage`), so a claim signature and a factor answer never
verify as each other. Go callers use
`SignShareClaim` (`sdk.SignShareClaim`), with `ShareClaimMessage` and the
label helpers (`SystemClaimLabel`, `RecoveryClaimLabel`, `TenantClaimLabel`,
`DatabaseClaimLabel`).

Quorum challenge request and response:

```json
{ "domain": "unseal", "op": "unseal", "username": "carol", "factor": "" }
```

```json
{ "challenge_id": "...", "domain": "unseal", "op": "unseal", "username": "carol",
  "factor": "fido2", "nonce": "<base64>", "method_data": {}, "expires_at": "..." }
```

`factor` empty selects the designated factor. `op` is one of `unseal`, `seal`,
`root-token`, `rekey`, `recover`. `method_data.purpose` is the purpose the
challenge is bound to, `quorum:<claim label>:<domain>:<op>`; an `mtls` answer
is a signature over `qrdb/mtls-challenge/v1`, the `challenge_id`, the purpose
and the nonce, NUL-separated (`mtls.SignChallenge`), never over the bare
nonce. An `mtls` challenge's method data also carries the enrolled
`public_key` and `thumbprint`.

Quorum submission:

```json
{ "domain": "unseal", "op": "unseal", "username": "carol",
  "challenge_id": "...", "factor": "fido2", "response": "<base64>",
  "response_metadata": {}, "share": "<base64 share opened from the envelope>" }
```

Quorum outcome: `domain`, `op`, `required`, `submitted`, `complete`; on a
completed `root-token` round `root_token` and `root_token_created_at`; on a
completed `rekey` round `threshold`, `total_shares` and `share_count` of the
new split. **A below-threshold submission is a success, not an error.**

Start rekey: `{"domain": "unseal", "threshold": 3, "policy": "any-enrolled",
"so_pin": "..."}`. Revoke: `{"domain": "unseal", "username": "erin",
"threshold": 0, "so_pin": "..."}` (0 keeps the current threshold). Both answer
the open round: `domain`, `op`, `required`, `submitted`, `expires_at`,
`new_threshold`, `policy`, `remove`.

Shamir initialization: `{"threshold": 3, "total_shares": 5, "policy":
"designated"}`; the response is share-free, `{"threshold": 3, "total_shares":
5, "share_count": 5}`. `total_shares` must equal the number of enrolled
unseal-domain holders. Optional `so_pin` / `user_pin` provision the PIN
backend first; PINs supplied with no backend configured are refused, never
ignored. Recovery generation takes `threshold`, `policy` and
`so_pin` and splits to every enrolled recovery holder.

## Error classes

Every share-holder, quorum and envelope error is classified once, in the
one classification table every transport renders (`pkg/api/errclass`):

| Class | REST | gRPC / Unix | Typical cause |
|---|---|---|---|
| InvalidArgument | 400 | `INVALID_ARGUMENT` | unknown role, op or domain; bad CSR; unsupported KEM; malformed envelope |
| PermissionDenied | 403 | `PERMISSION_DENIED` | factor proof failed; factor not admitted by the policy; holder not in roster or being revoked; share index not the holder's; certificate mismatch; self-enrolment; reconstructed key fails to open the blob (`ErrUnsealKeyMismatch`) |
| NotFound | 404 | `NOT_FOUND` | unknown holder; no pending share; unknown factor |
| AlreadyExists | 409 | `ALREADY_EXISTS` | holder already submitted to this round; duplicate username or key; a round already in progress |
| FailedPrecondition | 409 | `FAILED_PRECONDITION` | no round open; round expired; share already claimed; revocation would leave fewer holders than the threshold; roster missing; no verifier or CA; a rolled-back initialization awaiting a restart (`ErrRestartRequired`); a holder registered while the initialized barrier is sealed (`ErrRegistrationWhileSealed`); a holder registered past a database barrier's N before initialization (`HolderLimitReachedError`) |
| Gone | 410 | `FAILED_PRECONDITION` | the envelope outlived its claim window and was destroyed (`ceremony.ErrShareExpired`); only a rekey deals a new one |
| PreconditionFailed | 412 | `FAILED_PRECONDITION` | no PIN backend is configured (`pin.ErrBackendNotConfigured`); rekey start or revocation before an SO PIN is provisioned (`pin.ErrPINNotSet`) |
| Unprocessable | 422 | `FAILED_PRECONDITION` | a Shamir barrier unsealed or sealed by one caller instead of its quorum; no ceremony service configured; too few officers |
| ResourceExhausted | 429 | `RESOURCE_EXHAUSTED` | the SO PIN check locked out after repeated failures (`pin.SOPINLockedError`); quorum challenges requested, or shares submitted, faster than the caller's throttle allows (`QuorumRateLimitedError`). Both carry the wait as REST `Retry-After`, a gRPC / Unix `google.rpc.RetryInfo` status detail, MCP `retry_after_seconds` in the error envelope, and a `google.rpc.RetryInfo` detail in the QUIC error frame's payload |
| Aborted | 409 | `ABORTED` | the roster was replaced while the submission or rekey was prepared (`StaleRosterError`); a holder registered or unenrolled, or a second initialization, while the barrier is being initialized or its recovery key generated (`ErrInitializationInProgress`); retry |
| Unauthenticated | 401 | `UNAUTHENTICATED` | a claim over a connection without a verified client certificate (`service.ClientCertRequiredError`) |
| Internal | 500 | `INTERNAL` | an initialization whose distribution failed could not be rolled back (`ShamirInitRollbackError`) |

QUIC and MCP carry the gRPC code name, and the embedded transport returns
the classified error itself. A claim without a client certificate is
Unauthenticated on every network transport. An SO-PIN-gated route answers 401 for a
wrong PIN and 412 when no PIN backend is configured — it fails closed.

## Versioning

All custody REST paths are under `/v1/`.
