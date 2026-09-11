# Authentication Factors and PINs — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Two distinct gRPC services, because there are two distinct factor models:

- **`qrdb.v1.AuthFactorService`** — ceremony-time custodian enrolment.
- **`qrdb.v1.UserAuthFactorService`** — the user-side N-factor model.

The Unix socket transport serves the same gRPC wire over a UDS.

## Capability matrix — user-side N-factor (`Client.UserAuthFactors()`)

| Capability | REST | gRPC / Unix (`UserAuthFactorService/`) | QUIC | MCP tool | `scp` permission |
|---|---|---|---|---|---|
| Add | `POST /v1/users/factors` | `Add` | `userauthfactor.Add` | `qrdb_user_authfactor_add` | `userauthfactors:add` |
| List | `GET /v1/users/factors` | `List` | `userauthfactor.List` | `qrdb_user_authfactor_list` | `userauthfactors:list` |
| Get | `GET /v1/users/factors/{id}` | `Get` | `userauthfactor.Get` | `qrdb_user_authfactor_get` | `userauthfactors:get` |
| Remove | `DELETE /v1/users/factors/{id}` | `Remove` | `userauthfactor.Remove` | `qrdb_user_authfactor_remove` | `userauthfactors:remove` |
| Rotate | `POST /v1/users/factors/{id}/rotate` | `Rotate` | `userauthfactor.Rotate` | `qrdb_user_authfactor_rotate` | `userauthfactors:rotate` |
| Relabel | `POST /v1/users/factors/{id}/relabel` | `Relabel` | `userauthfactor.Relabel` | `qrdb_user_authfactor_relabel` | `userauthfactors:relabel` |
| Login | `POST /v1/users/factors/login` | `Login` | `userauthfactor.Login` | `qrdb_user_authfactor_login` | `userauthfactors:login` |

## Capability matrix — ceremony custodian factors (`Client.AuthFactors()`)

| Capability | REST | gRPC / Unix (`AuthFactorService/`) | QUIC | MCP tool | `scp` permission |
|---|---|---|---|---|---|
| Enroll | `POST /v1/authfactors` | `EnrollFactor` | `authfactor.Enroll` | `qrdb_authfactor_enroll` | `authfactors:enroll` |
| Challenge | `POST /v1/authfactors/challenge` | `ChallengeFactor` | `authfactor.Challenge` | `qrdb_authfactor_challenge` | `authfactors:challenge` |
| Verify | `POST /v1/authfactors/verify` | `VerifyFactor` | `authfactor.Verify` | `qrdb_authfactor_verify` | `authfactors:verify` |
| List | `GET /v1/authfactors` | `ListFactors` | `authfactor.List` | `qrdb_authfactor_list` | `authfactors:list` |
| Revoke | `DELETE /v1/authfactors/{factor}` | `RevokeFactor` | `authfactor.Revoke` | `qrdb_authfactor_revoke` | `authfactors:revoke` |

**`Client.UserAuthFactors()` returns nil on transports that do not implement
it** — notably the embedded transport, which currently exposes only the legacy
`AuthFactorClient` surface. Nil-check it; the nil is not an error value. The
same applies to `Factors()`, `Login()` and `Sessions()`.

> **Known gap.** The auth-factor and audit handlers across REST, QUIC, MCP,
> Unix and the SDK transports currently carry **hardcoded permission strings**
> pending `permission.AuthFactor*` and `permission.AuditQuery` constants. The
> permissions above name the intended constants; the code does not yet use them.
> That is a tracked defect, not a naming choice.

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
  leaving the key absent — absent is what a record written before the field
  existed looks like, and login policy must be able to tell the two apart.
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
  `Get`, `Remove`, `Rotate`, `Relabel`. There is no cross-tenant form of these
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
| Removing the last factor without force | 409 | `FAILED_PRECONDITION` |
| Unknown factor type or method | 400 | `INVALID_ARGUMENT` |
| Metadata key inside the server-owned `attest_*` namespace | 400 | `INVALID_ARGUMENT` — `ErrReservedMetadata` |
| Login tuple resolves to no user | 401 | `UNAUTHENTICATED` |
| PIN shorter than 6 characters | 400 | `INVALID_ARGUMENT` — `ErrPINTooShort` |
| PIN does not verify | 403 | `PERMISSION_DENIED` — `ErrPINInvalid` |
| One PIN of a required pair supplied | 400 | `INVALID_ARGUMENT` — dual control |
| Backend locked out | 429 | `RESOURCE_EXHAUSTED` — `ErrTPMLocked` / `ErrPINLocked`, carrying `LockoutStatus` |
| Missing permission | 403 | `PERMISSION_DENIED` |

Every PIN failure path fails closed. Note that the **software backend has no
lockout**: `GetLockoutStatus()` returns nil and `ResetLockout()` is a no-op, so
a nil status means "this backend has no lockout", **not** "not locked out".

## Versioning

Factor REST paths are under `/v1/`. Nothing here is deprecated.
