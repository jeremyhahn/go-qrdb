# Shamir Custody — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-01

gRPC service `qrdb.v1.QRDBService`. The Unix socket transport serves the same
gRPC wire over a UDS, so its methods match the gRPC column.

The barrier's own operations are [../barrier-seal/api.md](../barrier-seal/api.md).

## Capability matrix — Security Officers

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded | `scp` permission |
|---|---|---|---|---|---|---|
| Register officer | `POST /v1/barrier/officers` | `BarrierOfficerRegister` | `barrier.OfficerRegister` | `qrdb_barrier_officer_register` | `OfficerRegister` | `seal:officer_register` |
| List officers | `GET /v1/barrier/officers` | `BarrierOfficerList` | `barrier.OfficerList` | `qrdb_barrier_officer_list` | `OfficerList` | `seal:officer_list` |
| Claim status | `GET /v1/barrier/officers/claim-status` | `BarrierOfficerClaimStatus` | `barrier.OfficerClaimStatus` | `qrdb_barrier_officer_claim_status` | `OfficerClaimStatus` | `seal:officer_list` |
| Claim begin | `POST /v1/barrier/officers/{username}/claim/begin` | `BarrierShareClaimBegin` | `barrier.ShareClaimBegin` | `qrdb_barrier_share_claim_begin` | `ShareClaimBegin` | **identity-gated** |
| Claim complete | `POST /v1/barrier/officers/{username}/claim/complete` | `BarrierShareClaimComplete` | `barrier.ShareClaimComplete` | `qrdb_barrier_share_claim_complete` | `ShareClaimComplete` | **identity-gated** |

## Capability matrix — quorum operations

| Capability | REST | gRPC / Unix | QUIC | MCP tool | `scp` permission |
|---|---|---|---|---|---|
| Initialize Shamir | `POST /v1/barrier/shamir/initialize` | `BarrierShamirInitialize` | `barrier.ShamirInitialize` | `qrdb_barrier_shamir_initialize` | `seal:initialize_shamir` |
| Unseal — one share | `POST /v1/barrier/shamir/unseal-share` | `BarrierShamirUnsealShare` | `barrier.ShamirUnsealShare` | `qrdb_barrier_shamir_unseal_share` | `barrier:unseal-share` |
| Unseal — many shares | `POST /v1/barrier/shamir/unseal-shares` | `BarrierShamirUnsealShares` | `barrier.ShamirUnsealShares` | `qrdb_barrier_shamir_unseal_shares` | `barrier:unseal-share` |
| Seal vote | `POST /v1/barrier/shamir/seal-vote` | `BarrierShamirSealVote` | `barrier.ShamirSealVote` | `qrdb_barrier_shamir_seal_vote` | `seal:submit_seal_vote` |
| Rekey | `POST /v1/barrier/rekey` | `BarrierRekey` | `barrier.Rekey` | `qrdb_barrier_rekey` | `seal:rekey` |
| Root token | `POST /v1/barrier/root-token` | `BarrierRootToken` | `barrier.RootToken` | `qrdb_barrier_root_token` | `seal:bootstrap` |

## Capability matrix — recovery

| Capability | REST | gRPC / Unix | QUIC | MCP tool | `scp` permission |
|---|---|---|---|---|---|
| Generate recovery keys | `POST /v1/barrier/recovery/generate` | `BarrierRecoveryGenerate` | `barrier.RecoveryGenerate` | `qrdb_barrier_recovery_generate` | `seal:generate_recovery` |
| Recover | `POST /v1/barrier/recovery/recover` | `BarrierRecoveryRecover` | `barrier.RecoveryRecover` | `qrdb_barrier_recovery_recover` | `seal:recover_with_keys` |
| Recovery status | `GET /v1/barrier/recovery/status` | `BarrierRecoveryStatus` | `barrier.RecoveryStatus` | `qrdb_barrier_recovery_status` | `seal:status` |
| Delete recovery keys | `DELETE /v1/barrier/recovery` | `BarrierRecoveryDelete` | `barrier.RecoveryDelete` | `qrdb_barrier_recovery_delete` | `seal:generate_recovery` |
| Register recovery officer | `POST /v1/barrier/recovery/officers` | `BarrierRecoveryOfficerRegister` | `barrier.RecoveryOfficerRegister` | `qrdb_barrier_recovery_officer_register` | `seal:officer_register` |
| Recovery claim begin | `POST /v1/barrier/recovery/officers/{username}/claim/begin` | `BarrierRecoveryShareClaimBegin` | `barrier.RecoveryShareClaimBegin` | `qrdb_barrier_recovery_share_claim_begin` | **identity-gated** |
| Recovery claim complete | `POST /v1/barrier/recovery/officers/{username}/claim/complete` | `BarrierRecoveryShareClaimComplete` | `barrier.RecoveryShareClaimComplete` | `qrdb_barrier_recovery_share_claim_complete` | **identity-gated** |

## Capability matrix — custodians and ceremony share claims

| Capability | REST | `scp` permission |
|---|---|---|
| Enroll custodian | `POST /v1/ceremony/enroll-custodian` | `ceremony:enroll_custodian` |
| Revoke custodian | `POST /v1/ceremony/revoke-custodian` | `ceremony:revoke_custodian` |
| Enroll external officer | `POST /v1/init/enroll-external-officer` | `ceremony:enroll_custodian` |
| Custodian claim begin/complete | `POST /v1/ceremony/claim-share/begin` · `/complete` | **identity-gated** |
| Officer share begin/complete | `POST /v1/ceremony/officer-share/begin` · `/complete` | **identity-gated** |
| Submit unseal share | `POST /v1/barrier/unseal-share` | `barrier:unseal-share` |

## Who may register and list officers

The officer registry decides who holds a share of the node barrier's key:
every rekey, revocation and recovery-key generation sends a share to every
registered officer. `seal:officer_register` (both the unseal and the recovery
domain), `seal:officer_list` (the list and the claim status),
`ceremony:enroll_custodian` (custodians and external officers),
`ceremony:revoke_custodian`, `ceremony:initialize` and `ceremony:sign_csr` are
therefore **cross-scope**: only an identity of the `system` tenant may exercise
them, on every transport, and a tenant's SO or Admin is refused with `403` /
`PermissionDenied` even though its built-in role carries the grant. The quorum
and recovery operations above that change the node barrier (`seal:rekey`,
`seal:bootstrap`, `seal:submit_seal_vote`, `seal:generate_recovery`,
`seal:recover_with_keys`, `seal:initialize_shamir`) are cross-scope for the
same reason, and so is `barrier:unseal-share`: the node accumulator takes any
share it is given, and every tenant's built-in Custodian role carries the
grant, so a tenant custodian could poison an in-progress node unseal. Node
custodians are the `system` tenant's. `seal:status` is not cross-scope.

**This changed.** These permissions were self-scoped, and because none of
these routes names a tenant, the self-scope check compared the caller's tenant
with itself and admitted any tenant's holder. A tenant role that relied on that
can no longer register, list or enrol node officers; a tenant's own officers
are registered through `POST /v1/tenants/{id}/barrier/officers`
(`tenant_barrier:officer_register`).

First-boot registration is not affected. On the boot-time sealed-window
listener officer and recovery-officer registration are anonymous while the
enrollment ceremony is pre-operational, and that listener runs no RBAC.

## Why the claim routes are identity-gated

Officers are **ceremony-registry principals, not RBAC users** — the user store
does not yet exist when they are registered. The middleware therefore requires
only an authenticated mTLS identity, and **the handler enforces the real
authorization**: registry membership, certificate-thumbprint match against the
enrolment record, and nonce-signature verification.

This is a deliberate design, recorded here so it is not mistaken for a missing
permission check.

## Schemas

Officer registration:

```json
{ "username": "alice", "csr": "<PEM>",
  "authenticator_type": "piv", "authenticator_ref": "slot9a" }
```

Response: the signed certificate and the officer's **share index** — never
share bytes.

Claim begin response: a nonce with a **5-minute TTL**.
Claim complete request: the username, the nonce, and the raw signature.
Claim complete response: the share bytes, **exactly once**; the server enclave
is destroyed.

Shamir initialization response — **share-free**:

```json
{ "threshold": 3, "total_shares": 5, "share_count": 5 }
```

Quorum progress, returned by single-share unseal and by seal votes:

```json
{ "required": 3, "submitted": 2, "complete": false }
```

**Below-threshold submission is a 200, not an error** — the shares accumulate.

Recovery generation response is share-free in the same shape.

Root token response: `{"token": "<hex HMAC-SHA256>"}`.

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success, including below-threshold progress | 200 | `OK` |
| Duplicate share | 409 | `ALREADY_EXISTS` — `ErrShamirDuplicateShare` |
| Quorum TTL elapsed (default 15m) | 410 | `DEADLINE_EXCEEDED` — `ErrShamirQuorumExpired` |
| Reconstructed key fails to open the wrapped root key | 403 | `PERMISSION_DENIED` — `ErrUnsealKeyMismatch` (wrong or rekey-revoked shares) |
| Wrapped blob malformed | 500 | `DATA_LOSS` — `ErrWrappedRootKeyCorrupt` |
| Threshold < 2 or > total | 400 | `INVALID_ARGUMENT` — `ErrShamirThresholdInvalid` |
| Single-admin `Seal` on a Shamir barrier | 409 | `FAILED_PRECONDITION` — `ErrShamirBarrierRequiresQuorum` |
| Seal-vote quorum fails verification | 403 | `PERMISSION_DENIED` — `ErrSealVoteVerificationFailed` |
| Init/rekey before `SetCeremonyDistributor` | 503 | `FAILED_PRECONDITION` — `ErrCeremonyDistributorNotConfigured` |
| Claim nonce expired or signature invalid | 403 | `PERMISSION_DENIED` |
| Claim for an already-claimed share | 409 | `FAILED_PRECONDITION` — delivered exactly once |
| No recovery key metadata | 404 | `NOT_FOUND` — `ErrRecoveryKeysNotFound` |
| Root token share verification failed | 403 | `PERMISSION_DENIED` — `ErrRootTokenVerificationFailed` |

## Versioning

All custody REST paths are under `/v1/`. Nothing here is deprecated.
