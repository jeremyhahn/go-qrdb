# Firstboot and System Bootstrap — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Firstboot is a **composition-plane capability**: it runs inside the daemon's
startup sequence, driven by `pkg/firstboot`'s `Dependencies`, not by a client
calling an endpoint. What clients see over the wire is the ceremony
(`POST /v1/ceremony/initialize`) plus two bootstrap-window routes.

## Capability matrix

| Capability | REST | gRPC / Unix | QUIC | MCP | Embedded | `scp` permission |
|---|---|---|---|---|---|---|
| Is the system initialized? | `GET /v1/config/initialized` | MISSING | MISSING | MISSING | — | **public** |
| Fetch the CA bootstrap certificate | `GET /v1/ca/bootstrap` | MISSING | MISSING | MISSING | — | **public**, conditionally registered |
| Run the bootstrap ceremony | `POST /v1/ceremony/initialize` | `CeremonyInitialize` | `ceremony.Initialize` | `qrdb_ceremony_initialize` | `Client.Ceremony().CeremonyInitialize` | `ceremony:initialize` |
| Firstboot itself (tenant, roles, admin, admin cert) | **in-process only** | — | — | — | `firstboot.Initialize` | n/a |

`GET /v1/config/initialized` and `GET /v1/ca/bootstrap` are **two of the very
few unauthenticated routes in the server**, and they exist precisely because a
client in the bootstrap window has no credentials yet. `/v1/ca/bootstrap` is
registered **only when a bootstrap certificate is configured**
(`len(s.caBootstrapCert) > 0`); otherwise the route does not exist.

The two public routes are REST-only, marked `MISSING` on the other transports
per the convention. That is defensible for `/v1/config/initialized` — a client
that has not yet fetched a CA bundle cannot complete an mTLS handshake on the
other transports anyway — but it is recorded rather than assumed correct.

`firstboot.Initialize` has **no wire surface at all**, by design: it is the
daemon bootstrapping itself, and exposing "create the platform admin" as an
endpoint would be a privilege-escalation surface with nothing to authorize it
against.

## Admin credential paths

Which path runs is decided by the `Dependencies` the daemon supplies, not by a
request field:

| Path | Inputs | Result |
|---|---|---|
| **C** adopt-presented — **checked first** | `AdminCertPEM` + `AdminCertPool` | validates and adopts; issues nothing |
| **B** CSR-sign | `AdminCSRPEM` + `Signer` | signs the admin's CSR |
| **A** generate-and-distribute | `CA` | issues a key + certificate bundle |

Refusals: `ConflictingAdminCertInputsError` (both CSR and certificate),
`MissingAdminCertPoolError` (certificate without a pool),
`AdminCertRejectedError` (unparseable, expired, or no chain to the pool).

## Ceremony initialize request

```json
{ "so_pin": "…", "user_pin": "…", "threshold": 3 }
```

With `threshold >= 2`, enrolled officers must have been named or the request
fails closed with `ErrNoOfficers`. In M-of-N the User PIN **is** the Shamir
secret. Both PINs go straight to the configured `pin.PINBackend` and never
reach disk in raw form.

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success | 200 | `OK` |
| Already initialized | 409 | `FAILED_PRECONDITION` — idempotent, creates nothing |
| `threshold >= 2` with no officers | 400 | `INVALID_ARGUMENT` — `ErrNoOfficers` |
| Conflicting admin certificate inputs | 400 | `INVALID_ARGUMENT` |
| Admin certificate without a pool | 400 | `INVALID_ARGUMENT` |
| Admin certificate rejected | 403 | `PERMISSION_DENIED` |
| Follower attempted creation | — | retried after replication (`NotLeader`) |
| No bootstrap certificate configured | 404 (`/v1/ca/bootstrap` unregistered) | — |

## Versioning

`/v1/config/initialized`, `/v1/ca/bootstrap` and `/v1/ceremony/initialize` are
under `/v1/`. Nothing here is deprecated.
