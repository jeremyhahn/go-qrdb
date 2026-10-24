# Authentication Factors and PINs — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

Two distinct gRPC services, because there are two distinct factor models:

- **`qrdb.v1.AuthFactorService`** — ceremony-time custodian enrolment.
- **`qrdb.v1.UserAuthFactorService`** — the user-side N-factor model.

The Unix socket transport serves the same gRPC wire over a UDS.

## Capability matrix — user-side N-factor (`Client.UserAuthFactors()`)

| Capability | REST | gRPC / Unix (`UserAuthFactorService/`) | QUIC | MCP tool | Embedded | `scp` permission |
|---|---|---|---|---|---|---|
| Add | `POST /v1/users/factors` | `Add` | `userauthfactor.Add` | `qrdb_user_authfactor_add` | `Client.UserAuthFactors().AddAuthFactor` | `user_authfactors:add` |
| List | `GET /v1/users/factors/list` | `List` | `userauthfactor.List` | `qrdb_user_authfactor_list` | `Client.UserAuthFactors().ListAuthFactors` | `user_authfactors:list` |
| Get | `GET /v1/users/factors/{id}` | `Get` | `userauthfactor.Get` | `qrdb_user_authfactor_get` | `Client.UserAuthFactors().GetAuthFactor` | `user_authfactors:get` |
| Remove | `DELETE /v1/users/factors/{id}` | `Remove` | `userauthfactor.Remove` | `qrdb_user_authfactor_remove` | `Client.UserAuthFactors().RemoveAuthFactor` | `user_authfactors:remove` |
| Delete | `POST /v1/users/factors/delete` | `Delete` | `userauthfactor.Delete` | `qrdb_user_authfactor_delete` | `Client.UserAuthFactors().DeleteAuthFactor` | `user_authfactors:add` |
| Rotate | `POST /v1/users/factors/rotate` | `Rotate` | `userauthfactor.Rotate` | `qrdb_user_authfactor_rotate` | `Client.UserAuthFactors().RotateAuthFactor` | `user_authfactors:rotate` |
| Relabel | `PUT /v1/users/factors/{id}/label` | `Relabel` | `userauthfactor.Relabel` | `qrdb_user_authfactor_relabel` | `Client.UserAuthFactors().RelabelAuthFactor` | `user_authfactors:relabel` |
| Login | `POST /v1/users/factors/login` | `Login` | `userauthfactor.Login` | `qrdb_user_authfactor_login` | `Client.UserAuthFactors().LoginAuthFactor` | `user_authfactors:login` |

The rotate route carries the factor in the request body, not the path.

### Delete — undoing an add

`Remove` revokes: the record stays, stamped `revoked_at`, and so do the
indexes that claim its credential and SubjectPublicKeyInfo, so a revoked
certificate can never be bound again by anyone. That is right for an
administrator withdrawing a credential, and wrong for a caller rolling back an
add it has just made, which would otherwise leave the credential claimed by a
factor that never served.

`Delete` is that rollback. It removes the factor's record, its user index
entry and every credential and SPKI index that names it, and moves the
user's factor-set version, in **one** guarded commit: all of it is gone, or
none of it is. Afterwards the same credential binds to the same user again,
as a new factor with a new id. It carries the permission of the `Add` it
undoes, `user_authfactors:add`, and is audited as `delete`.

| Field | REST and QUIC (JSON body) | gRPC / Unix (`UserAuthFactorDeleteRequest`) | MCP argument | Embedded / SDK | CLI |
|---|---|---|---|---|---|
| The factor to delete | `"factor_id"` | `factor_id` (1) | `factor_id` | `FactorID` | `<factor-id>` |
| Allow deleting the user's last active factor | `"force": true` | `force` (2) | `force` (boolean) | `Force` | `--force` |

The response is `{ "status": "deleted" }` (gRPC / Unix
`UserAuthFactorDeleteResponse.status`); MCP adds `factor_id`.

- **A revoked factor is refused** (`410` / `FAILED_PRECONDITION`,
  `ErrFactorRevoked`) and keeps every index it holds. Nothing reinstates an
  administrator's revocation: neither `Delete` nor delete-then-`Add`.
- **The last active factor needs `force`**, exactly as for `Remove`; with it
  the user is deactivated.
- **Deleting a factor that is not there is not found**, including the second
  of two deletes of the same id. A delete is never silently idempotent.
- An index entry that names a different factor is left alone: `Delete` frees
  only what this factor claimed.

### Conditional add — the one-time claim and the expected version

`Add` takes two optional conditions. With either set, the factor is enrolled
only if every condition that is set holds **at the moment the add commits** —
not when the caller last looked — so of N callers racing the same condition,
exactly one succeeds and every other one is refused with nothing enrolled.
It is the same verb with the same permission, `user_authfactors:add`, and the
same tenant routing as an unconditional add.

| Condition | REST and QUIC (JSON body) | gRPC / Unix (`UserAuthFactorAddRequest`) | MCP argument | Embedded / SDK | CLI |
|---|---|---|---|---|---|
| The user holds no active factor of the added `type` | `"if_no_active_type": true` | `if_no_active_type` (8) | `if_no_active_type` (boolean) | `IfNoActiveType` | `--if-no-active-type` |
| The user's factor set is at version N | `"expected_factor_set_version": N` | `optional uint64 expected_factor_set_version` (9) | `expected_factor_set_version` (decimal string) | `ExpectedFactorSetVersion *uint64` | `--expected-version N` |

**The factor-set version** is a per-user counter. It is `0` until the user's
factor set first changes, then moves on every committed `Add`, `Remove`,
`Delete` and `Rotate`. `List` reports it as `factor_set_version` (gRPC / Unix field 3 of
`UserAuthFactorListResponse`), read **before** the factors are listed: a set
that changes in between leaves the reported version behind the list, so an add
guarded on it is refused rather than decided against a set the caller never
saw. A conditional `Add` reports the version it committed as
`factor_set_version` (gRPC / Unix field 2 of `UserAuthFactorAddResponse`); an
unconditional `Add` reports `0`, which the REST body omits. The
read-modify-write loop is therefore:
`List` → decide → `Add` with `expected_factor_set_version` → on refusal, `List`
again.

**Setting both conditions** refuses the add if either fails; the version is
checked first. Sending neither is an ordinary unconditional add.

**`Rotate` takes no condition.** A rotation's new factor that names one is
refused with `400` / `INVALID_ARGUMENT` before anything is written — on REST,
QUIC and embedded at the server, and by every SDK client before it sends. The
gRPC / Unix and MCP rotate schemas have no condition fields to send. A
rotation silently dropping a guard would let its caller believe it had been
decided by one.

**A refused add is a structured refusal**, the same on every transport: the
class is `AlreadyExists` (`409` / `ALREADY_EXISTS`), the reason is
`FACTOR_CONDITION_FAILED` in the `qrdb.v1` domain, and the refusal's fields
travel as metadata:

| Metadata key | Meaning |
|---|---|
| `user_id` | The user the add targeted. |
| `factor_type` | The type the add would have enrolled. |
| `condition` | `no_active_of_type` or `expected_version` — which condition failed. |
| `expected_version` | The version the caller expected (`expected_version` refusals). |
| `current_version` | The factor-set version the add was decided against. |
| `existing_factor_id` | The active factor of `factor_type` that refused a `no_active_of_type` add. |

| Transport | Where the reason and metadata travel |
|---|---|
| REST | The error body's `code` and `metadata` fields, beside `error`. |
| gRPC / Unix | A `google.rpc.ErrorInfo` status detail. |
| QUIC | The error payload's `info` (`domain`, `reason`, `metadata`). |
| MCP | The error envelope's `reason` and `metadata`. |
| Embedded | The typed `*auth.FactorConditionFailedError` itself. |

Every SDK client decodes the reason back into
`*FactorConditionFailedError` (`sdk.FactorConditionFailedError`), carried as the
`Err` of the transport's `RequestError`, so
`errors.Is(err, sdk.ErrFactorConditionFailed)` and
`errors.As(err, &refusal)` behave identically on all six transports. A refusal
is distinguished from a duplicate credential (`ErrFactorAlreadyExists`, the
same class) by the reason, never by the message.

## Capability matrix — ceremony custodian factors (`Client.AuthFactors()`)

Every REST route below spells its path segment `{factor}`, and the segment
means something different under each verb: `handleAuthFactorEnroll` reads it
as the factor name, `handleAuthFactorChallenge` and `handleAuthFactorRevoke`
as the enrollment ID. `handleAuthFactorVerify` checks it for presence only;
the challenge it verifies comes from `challenge_id` in the request body.

| Capability | REST | gRPC / Unix (`AuthFactorService/`) | QUIC | MCP tool | Embedded | `scp` permission |
|---|---|---|---|---|---|---|
| Enroll | `POST /v1/authfactors/{factor}/enroll` | `EnrollFactor` | `authfactor.Enroll` | `qrdb_authfactor_enroll` | `Client.AuthFactors().EnrollFactor` | `authfactor:enroll` |
| Challenge | `POST /v1/authfactors/{factor}/challenge` | `ChallengeFactor` | `authfactor.Challenge` | `qrdb_authfactor_challenge` | `Client.AuthFactors().ChallengeFactor` | `authfactor:challenge` |
| Verify | `POST /v1/authfactors/{factor}/verify` | `VerifyFactor` | `authfactor.Verify` | `qrdb_authfactor_verify` | `Client.AuthFactors().VerifyFactor` | `authfactor:verify` |
| List | `GET /v1/authfactors/list` | `ListFactors` | `authfactor.List` | `qrdb_authfactor_list` | `Client.AuthFactors().ListFactors` | `authfactor:list` |
| Revoke | `DELETE /v1/authfactors/{factor}` | `RevokeFactor` | `authfactor.Revoke` | `qrdb_authfactor_revoke` | `Client.AuthFactors().RevokeFactor` | `authfactor:revoke` |

### The challenge id is opaque, and carries its own storage location

The `challenge_id` `Challenge` returns is `{bucket}.{factorChallengeID}`:
twelve decimal digits naming the challenge's expiry minute, a `.`, then the
identifier the factor itself issued. Treat it as opaque and pass it back to
`Verify` verbatim. The server derives the challenge's storage key from it
directly, so an id that does not have this shape is refused as not-found
without a lookup, on every transport, exactly like an id that names no
pending challenge:

| Condition | REST | gRPC / Unix | QUIC | MCP tool |
|---|---|---|---|---|
| `challenge_id` is malformed, unknown, already consumed, or reclaimed after expiry | 404 | `NOT_FOUND` | `StatusNotFound` | `CodeNotFound` |

The embedded transport returns the typed `*authfactors.EnrollmentNotFoundError`.
A challenge nobody verifies is reclaimed some minutes after it expires, by the
server itself; there is no call to cancel one.

### `Verify` is dispatched to the enrollment's factor, and `factor_name` is checked, not chosen

A challenge is verified by the factor its enrollment was made with. The
optional `factor_name` on `Verify` (the JSON body field on REST, the request
field on gRPC / Unix / QUIC, the tool argument on MCP, `FactorName` on
embedded) never selects the factor:

- **Empty** — the enrollment's factor verifies the response.
- **Equal to the enrollment's factor** — the same.
- **Anything else** (compared case-insensitively, as the registry compares
  factor names) — refused. The permissive factor is never consulted, and
  the challenge is consumed, so a refused attempt cannot be retried against
  it.

A challenge whose enrollment was revoked after it was issued is refused on
`Verify`, just as `Challenge` refuses a revoked enrollment.

| Condition | REST | gRPC / Unix | QUIC | MCP tool |
|---|---|---|---|---|
| `factor_name` differs from the enrollment's factor | 400 | `INVALID_ARGUMENT` | `StatusInvalidArgument` | `CodeInvalidArgument` |
| enrollment revoked since the challenge was issued | 410 | `FAILED_PRECONDITION` | `StatusFailedPrecondition` | `CodeFailedPrecondition` |

The embedded transport returns the typed `*authfactors.FactorMismatchError`
(`AuthFactorMismatchError` in `sdk/go`) and `*authfactors.EnrollmentRevokedError`.

### Enrolment is scoped to the caller's tenant, and `tenant_id` is checked, not chosen

Every one of these five verbs routes on the caller's authenticated context
tenant, exactly like the user-side surface above — a custodian enrolled under
one tenant's context is invisible to `Challenge` / `Verify` / `List` / `Revoke`
calls made under another's, and there is no cross-tenant form of any of them.

`Enroll`'s `tenant_id` field does not select the tenant; it is checked
against the one the call's context already resolved to:

- **Empty** — filled in from the context tenant.
- **Equal to the context tenant** — passes unchanged.
- **Anything else** (compared case-insensitively, as the registry compares
  factor names) — refused. The record would otherwise physically land in
  the context tenant while the field claimed a different one, and correcting
  it silently would hide that the caller's belief about its own tenant was
  wrong rather than surface it.

| Condition | REST | gRPC / Unix | QUIC | MCP tool |
|---|---|---|---|---|
| `tenant_id` names a tenant other than the caller's context | 400 | `INVALID_ARGUMENT` | `StatusInvalidArgument` | `CodeInvalidArgument` — `AuthFactorTenantMismatchError` |

The embedded transport returns the typed `AuthFactorTenantMismatchError`
(`sdk.AuthFactorTenantMismatchError`) directly. Every wire transport
classifies it through the one classification table every transport renders
(`pkg/api/errclass`), which classes it `InvalidArgument`; each transport's
row above is that class in its own vocabulary, so no transport carries a
clause of its own for it (see
[engineering.md](engineering.md#the-custodian-surface-is-routed-too)).

A resolved tenant that is sealed classifies the same way, on every verb that
can reach it — including `List`, where a seal between the store's `List` call
and one of its per-record `Get`s surfaces wrapped in `AuthFactorListReadError`
rather than as a silently short result. The condition can arrive as either of
two distinct sentinels, and both classify identically: `tenant.ErrTenantSealed`
is what `Resolve` returns when the tenant is ALREADY sealed; `seal.ErrSealed`
is what the resolved store's own `Get`/`Put` return when the barrier seals
AFTER `Resolve` succeeds but before the store operation runs (a live,
per-call check, not a cached one).

| Condition | REST | gRPC / Unix | QUIC | MCP tool |
|---|---|---|---|---|
| The caller's tenant is sealed (`tenant.ErrTenantSealed` or `seal.ErrSealed`) | 503 | `UNAVAILABLE` | `StatusUnavailable` | `CodeUnavailable` |

The embedded transport returns whichever of the two typed errors it received
unchanged. `tenant.ErrTenantSealed` is exported to the SDK as
`sdk.ErrTenantStoreSealed` — not `sdk.ErrTenantSealed`, which aliases the
distinct `pkg/seal.ErrTenantSealed` raised by barrier operations rather than
by store resolution. `seal.ErrSealed` is separately exported as
`sdk.ErrSealed`. The two read alike but are checked with `errors.Is` against
different values; a consumer must recognize both to catch this condition on
every path that can produce it.

**`Client.UserAuthFactors()` returns nil on transports that do not implement
it.** Every transport this SDK ships — REST, gRPC, QUIC, MCP, Unix, and
embedded — implements `transport.UserAuthFactorClient`, including embedded,
which also implements the ceremony-time `AuthFactorClient` surface above; a
caller reaching nil here is using a transport built outside this repository.
Nil-check it anyway; the nil is not an error value. The same nil-check
applies to `Factors()`, `Login()` and `Sessions()`, whose transport coverage
is not this feature's claim to make.

## PINs

**PINs have no wire capability of their own.** They are set and verified as part
of ceremony and barrier operations:

| Reached through | Where |
|---|---|
| `POST /v1/ceremony/initialize` (`so_pin`, `user_pin`) | [../ceremony/api.md](../ceremony/api.md) |
| `POST /v1/barrier/initialize` (`so_pin`, `user_pin`) | [../barrier-seal/api.md](../barrier-seal/api.md) |
| `POST /v1/barrier/seal`, `/rekey`, `/recovery/generate` (`so_pin`) | [../barrier-seal/api.md](../barrier-seal/api.md) |

That is deliberate: a PIN is an authorization input to a privileged operation,
not an object with its own CRUD surface.

## Schemas

Factor add / rotate:

```json
{ "user_id": 42, "type": "fido2", "label": "yubikey-5",
  "credential": "<hex>", "device": "YubiKey 5 NFC",
  "reader": "", "slot": "9a", "created_by": "self" }
```

`type`: `mtls` · `fido2` · `piv`. `created_by`: `self` · `admin` · `ceremony`.
`slot` defaults to `9a` for PIV.

`Add` alone also accepts the two condition fields described under
[Conditional add](#conditional-add--the-one-time-claim-and-the-expected-version):

```json
{ "user_id": 42, "type": "fido2", "label": "yubikey-5",
  "credential": "<hex>", "if_no_active_type": true,
  "expected_factor_set_version": 3 }
```

The `Add` response is `{ "factor": { … }, "factor_set_version": 4 }`; the
`List` response is `{ "factors": [ … ], "total": N, "factor_set_version": V }`.

Login resolves a `(method, lookup key)` tuple to a user:

| Method | Lookup key |
|---|---|
| `mtls` | SHA-256 of the certificate DER |
| `fido2` | WebAuthn `credentialID` bytes |
| `piv` | SHA-256 of the slot certificate DER |

**This RPC performs no hardware verification.** It maps the tuple to a user.
Hardware verification is the caller's — go-xkms's — responsibility.

Enrolment data is stored as an **opaque blob**. qrdb never parses it.

### The `attest_*` metadata namespace is server-owned

`metadata` is free-form with one exception: **any key beginning `attest_` is
refused on `Add` and on `Rotate`, over every transport.**

Those keys record what an enrolment ceremony actually *proved* about an
authenticator — `hardware` only when an attestation chain verified against
operator-configured roots — and login policy reads that record to decide
whether a credential may authenticate an administrator. A caller who could set
one would not be bypassing that control, they would be supplying its input: a
forged hardware claim wearing the field name the policy trusts.

It is a **prefix**, not a list of today's field names, so a future attestation
field is covered the day it is invented rather than the day someone remembers
to extend a list. Matching is case-insensitive: `Attest_Level` is refused too.

The request is **refused, not silently stripped**. A caller who set the key and
received a success would reasonably believe it was recorded, leaving the caller
and the store disagreeing with no signal to either. The error names both the
offending key and the reserved prefix, so an operator can tell a forged claim
from an ordinary naming collision.

Note the boundary is the literal prefix: `attestation_root` is *not* reserved,
because it does not begin `attest_`.

### What the server records in it

The namespace is refused as *input* and always written as *output*:

- **Every `Add` and `Rotate`, over every transport, records
  `attest_level=none`.** A factor written without a ceremony proved nothing
  about where its key lives, and the server says so positively rather than
  leaving the key absent, so login policy can tell a stamped record from an
  unstamped one.
  The response carries the persisted metadata, so a caller sees the stamp.
- **A verified attestation is recorded through `service.AttestationRecorder`
  (`sdk.AttestationRecorder`), and only there.** It is a service-layer
  interface the production user auth-factor service implements alongside
  `UserAuthFactorServicer`, reached by type assertion by the process that
  hosts the service — the composer that ran the WebAuthn or PIV ceremony and
  verified the attestation chain. `AddAttested` / `RotateAttested` take a
  typed `VerifiedAttestation{Level, AAGUID}` and stamp `attest_level` and
  `attest_aaguid` from it. **No transport carries this verb.** There is no
  RPC, no header and no flag that produces a `VerifiedAttestation`; a wire
  caller has exactly one path, and it records `none`.
- The trusted path refuses caller-supplied `attest_*` keys in `metadata`
  exactly as `Add` does, and refuses a `Level` outside the defined vocabulary
  (`none`, `software`, `hardware`) with `ErrInvalidAttestation` before
  anything is written. The typed attestation is the only input.

## Every call is scoped to the caller's tenant

The user-side factor surface is per-tenant IAM, and the tenant is the store the
call is routed to rather than anything in the request:

- **A login resolves only credentials enrolled in the tenant the login names.**
  The same `(method, lookup key)` presented under a different tenant is a
  `404` / `NOT_FOUND`, not somebody else's factor.
- **Two tenants may enrol the same credential.** Neither is refused for a
  collision with the other, and neither learns the other exists.
- **A factor id from another tenant is not found**, on every by-id verb —
  `Get`, `Remove`, `Delete`, `Rotate`, `Relabel`. There is no cross-tenant form of these
  calls.
- **Enrolment follows the user, not the caller's context.** `Add` places the
  record in the tenant that owns the named user id. A caller that may not reach
  that tenant is refused (`403` / `PERMISSION_DENIED`) rather than having the
  record redirected into its own tenant.
- **`List` for a user in another tenant returns an empty list**, not a denial:
  answering differently would confirm, to a caller who supplied only an id,
  both that the user exists and where it lives.
- **A sealed tenant refuses** with `503` / `UNAVAILABLE` on every verb. It never
  falls back to a readable store.

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success | 200 | `OK` |
| Factor not found, including one held by another tenant | 404 | `NOT_FOUND` |
| Enrolling a factor for a user in a tenant the caller may not reach | 403 | `PERMISSION_DENIED` — `*tenant.CrossTenantDeniedError` |
| The caller's tenant is sealed | 503 | `UNAVAILABLE` — `tenant.ErrTenantSealed` |
| Removing or deleting the last factor without force | 409 | `FAILED_PRECONDITION` |
| Deleting a revoked factor | 410 | `FAILED_PRECONDITION` — `*auth.FactorRevokedError` |
| Conditional add refused: an active factor of the type exists, or the factor-set version moved | 409 | `ALREADY_EXISTS` — `*auth.FactorConditionFailedError`, reason `FACTOR_CONDITION_FAILED` |
| An `Add`, `Remove`, `Delete` or `Rotate` lost the factor-set commit 64 times in a row to other changes to the same user's factors; retry | 409 | `ABORTED` — `*service.FactorSetContendedError` |
| A rotation's new factor carries an add condition | 400 | `INVALID_ARGUMENT` |
| Conditional add on a server composed over a store without compare-and-swap (never a production store) | 500 | `INTERNAL` — `ErrUserAuthFactorAtomicStoreRequired` |
| Delete on a server composed over a store without a multi-key guarded batch (never a production store) | 500 | `INTERNAL` — `ErrUserAuthFactorBatchStoreRequired` |
| Unknown factor type or method | 400 | `INVALID_ARGUMENT` |
| Metadata key inside the server-owned `attest_*` namespace | 400 | `INVALID_ARGUMENT` — `ErrReservedMetadata` |
| Login tuple resolves to no user | 401 | `UNAUTHENTICATED` |
| PIN shorter than 6 characters | 400 | `INVALID_ARGUMENT` — `ErrPINTooShort` |
| PIN does not verify | 403 | `PERMISSION_DENIED` — `ErrPINInvalid` |
| One PIN of a required pair supplied | 400 | `INVALID_ARGUMENT` — dual control |
| Backend locked out | 429 | `RESOURCE_EXHAUSTED` — `ErrTPMLocked` / `ErrPINLocked`, carrying `LockoutStatus` |
| Missing permission | 403 | `PERMISSION_DENIED` |

Every PIN failure path fails closed. Note that the **software backend has no
lockout of its own**: `GetLockoutStatus()` returns nil and `ResetLockout()` is a
no-op, so a nil status means "this backend has no lockout", **not** "not locked
out". The SO PIN lockout is `pin.NewLockoutBackend(backend) PINBackend`, a
decorator a node installs once around its backend; while locked it refuses
every SO PIN check with `*pin.SOPINLockedError{RetryAfter}` (unwraps to
`ErrPINLocked`; 429 / `RESOURCE_EXHAUSTED`).

## Versioning

Factor REST paths are under `/v1/`. Nothing here is deprecated.
