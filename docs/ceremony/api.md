# Initialization Ceremony — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

gRPC service `qrdb.v1.QRDBService`. The Unix socket transport serves the same
gRPC wire over a UDS, so its methods match the gRPC column.

## Capability matrix

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded (`Client.Ceremony()`) | `scp` permission |
|---|---|---|---|---|---|---|
| Initialize | `POST /v1/ceremony/initialize` | `CeremonyInitialize` | `ceremony.Initialize` | `qrdb_ceremony_initialize` | `CeremonyInitialize` | `ceremony:initialize` |
| Status | `GET /v1/ceremony/status` | `CeremonyStatus` | `ceremony.Status` | `qrdb_ceremony_status` | `CeremonyStatus` | `ceremony:status` |
| Sign CSR | `POST /v1/ceremony/sign-csr` | `CeremonySignCSR` | `ceremony.SignCSR` | `qrdb_ceremony_sign_csr` | `CeremonySignCSR` | `ceremony:sign_csr` |
| Claim cert — begin | `POST /v1/ceremony/claim-cert/begin` | `CeremonyClaimCertBegin` | `ceremony.ClaimCertBegin` | `qrdb_ceremony_claim_cert_begin` | `CeremonyClaimCertBegin` | `ceremony:claim_cert` |
| Claim cert — complete | `POST /v1/ceremony/claim-cert/complete` | `CeremonyClaimCertComplete` | `ceremony.ClaimCertComplete` | `qrdb_ceremony_claim_cert_complete` | `CeremonyClaimCertComplete` | `ceremony:claim_cert` |
| Officer share — begin | `POST /v1/ceremony/officer-share/begin` | `CeremonyClaimShareBegin` | `ceremony.ClaimShareBegin` | `qrdb_ceremony_claim_share_begin` | `CeremonyClaimShareBegin` | **identity-gated** |
| Officer share — complete | `POST /v1/ceremony/officer-share/complete` | `CeremonyClaimShareComplete` | `ceremony.ClaimShareComplete` | `qrdb_ceremony_claim_share_complete` | `CeremonyClaimShareComplete` | **identity-gated** |
| Custodian share — begin | `POST /v1/ceremony/claim-share/begin` | `CeremonyBeginClaimShare` | `ceremony.BeginClaimShare` | `qrdb_ceremony_begin_claim_share` | `CeremonyBeginClaimShare` | **identity-gated** |
| Custodian share — complete | `POST /v1/ceremony/claim-share/complete` | `CeremonyCompleteClaimShare` | `ceremony.CompleteClaimShare` | `qrdb_ceremony_complete_claim_share` | `CeremonyCompleteClaimShare` | **identity-gated** |
| Submit unseal share | `POST /v1/barrier/unseal-share` | `CeremonySubmitUnsealShare` | `ceremony.SubmitUnsealShare` | `qrdb_ceremony_submit_unseal_share` | `CeremonySubmitUnsealShare` | `barrier:unseal-share` |
| Enroll custodian | `POST /v1/ceremony/enroll-custodian` | — (see note) | — | — | — | `ceremony:enroll_custodian` |
| Revoke custodian | `POST /v1/ceremony/revoke-custodian` | — | — | — | — | `ceremony:revoke_custodian` |
| Enroll external officer | `POST /v1/init/enroll-external-officer` | `CeremonyEnrollExternalOfficer` | `ceremony.EnrollExternalOfficer` | — | `CeremonyEnrollExternalOfficer` | `ceremony:enroll_custodian` |

**Two distinct share-claim flows.** The *officer* pair
(`/v1/ceremony/officer-share/*`) is signature-verified against the officer's
enrolled certificate. The *custodian* pair (`/v1/ceremony/claim-share/*`) is
authentication-factor-verified. They are different protocols with different
inputs, not aliases — the CLI reflects this as
`claim-share begin/complete` versus `claim-share-begin`/`claim-share-complete`.

Custodian enrolment and revocation are REST-registered; their coverage on the
other transports is not established by the route table alone, so treat the
dashes above as **unverified**, not as a confirmed gap.

## Ceremony states

`CeremonyStatus` returns one of:

| State | Meaning |
|---|---|
| `awaiting_init` | freshly deployed, no ceremony performed |
| `enrolling` | in progress; officers claiming certificates and shares |
| `operational` | all officers have claimed; ready |

Single-admin (`threshold < 2`) goes straight from `awaiting_init` to
`operational`.

## Schemas

`POST /v1/ceremony/initialize` — `CeremonyInitializeRequest`:

```json
{ "so_pin": "…", "user_pin": "…", "threshold": 3 }
```

**The SO and User PIN values are handed straight to the configured
`pin.PINBackend`.** They leave the wire encrypted by TLS and never hit disk in
raw form. In M-of-N the User PIN **is** the Shamir secret.

`threshold >= 2` requires enrolled officers to have been named; a request
without them fails closed with the typed `ErrNoOfficers`.

Claim-cert begin returns a nonce; complete takes `{username, nonce,
signature}` (base64 over the raw nonce bytes) and returns the signed
certificate PEM.

Custodian claim-share begin returns a `challenge_id`; complete takes
`{custodian_id, challenge_id, factor_response}` (base64) and returns the sealed
share envelope.

`POST /v1/barrier/unseal-share` takes `{custodian_id, share, challenge_id,
factor_response}` — the factor response is optional here — and reports quorum
progress.

## Bootstrap-window routes

Two routes exist so a client can reach a system that is not yet operational:

| Route | Auth | Purpose |
|---|---|---|
| `GET /v1/config/initialized` | **public** | has this system been initialized? |
| `GET /v1/ca/bootstrap` | **public**, registered only when a bootstrap cert is configured | fetch the CA bootstrap certificate |

These are two of the very few unauthenticated routes in the server, and
`/v1/ca/bootstrap` exists only when the deployment configures a bootstrap
certificate.

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success | 200 | `OK` |
| `threshold >= 2` with no officers | 400 | `INVALID_ARGUMENT` — `ErrNoOfficers` |
| Share already claimed | 409 | `FAILED_PRECONDITION` — delivered exactly once |
| Nonce expired or signature invalid | 403 | `PERMISSION_DENIED` |
| Auth-factor response invalid | 403 | `PERMISSION_DENIED` |
| Invalid authenticator type | 400 | `INVALID_ARGUMENT` — `ErrInvalidAuthenticatorType` |
| No `CeremonyCA` wired | 503 | `FAILED_PRECONDITION` |
| Ceremony already operational | 409 | `FAILED_PRECONDITION` |
| Missing permission | 403 | `PERMISSION_DENIED` |

## Versioning

Ceremony REST paths are under `/v1/`. Nothing here is deprecated, but note that
the CLI's `qrdb ceremony init` verb has been **removed** in favour of the root
`qrdb init`.
