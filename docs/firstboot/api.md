# Firstboot and System Bootstrap — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-22
> Conflicts with this document are defects. Fix them here first.

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
registered **only when exactly one bootstrap source is configured**
(`WithCABootstrap` or `WithCABootstrapProvider`); with none the route does not
exist, and two are refused at start.

The two public routes are REST-only, marked `MISSING` on the other transports
per the convention: a client that has not yet fetched a CA bundle cannot
complete an mTLS handshake on the other transports.

`firstboot.Initialize` has **no wire surface at all**, by design: it is the
daemon bootstrapping itself, and exposing "create the platform admin" as an
endpoint would be a privilege-escalation surface with nothing to authorize it
against.

## Admin credential paths

The bootstrap admin's credential path is selected by request fields on
`CeremonyInitialize` itself, resolved identically on every transport (REST,
gRPC, Unix, QUIC, MCP, embedded all share the same conversion:
`pkg/api/holderwire` for REST/MCP/embedded, `pkg/api/holderpb` for
gRPC/Unix/QUIC). Exactly one must be set; there is no credential-less
default:

| Path | Request field(s) | Result |
|---|---|---|
| Adopt-presented (BYOC) | `admin_cert_pem` (+ optional `admin_cert_pool_pem`) | validates and adopts; issues nothing; needs no CA |
| CSR-sign | `admin_csr_pem` | the ceremony CA signs it; the admin's key never leaves their machine |
| Generate | `admin_generate: true` | the server generates a key pair and returns the key exactly once; dev/test/evaluation only |

Refusals: `ConflictingAdminCredentialInputsError` (more than one of the three
set), `NoAdminCredentialSelectedError` (none set), `AdminCredentialRejectedError`
(the presented certificate or pool is unparseable, expired, or does not
chain), `AdminCredentialRequiresCAError` (CSR-sign or generate selected on a
BYO-TLS node with no configured `CeremonyCA` — use BYOC instead).

## Ceremony initialize request

```json
{
  "so_pin": "…", "user_pin": "…", "threshold": 3,
  "admin_generate": true, "secret": "…"
}
```

With `threshold >= 2`, `officers` must name at least one share holder or the
request fails closed with `ErrNoOfficers`. Both PINs go straight to the
configured `pin.PINBackend` and never reach disk in raw form. `secret` is the
system barrier's own passphrase, base64-encoded in JSON, required exactly
when the configured barrier strategy needs one (e.g. `passphrase`) and empty
for `none`; `qrdb init` brings the system barrier up in this same call.

The response carries `admin_cert_pem` and `admin_credential_source`, which
names the path taken: `csr`, `byoc` or `generated`. `admin_key_pem` is set
only on the generate path -- the key is returned exactly once and never
persisted.

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success | 200 | `OK` |
| Already initialized | 409 | `FAILED_PRECONDITION` — idempotent, creates nothing |
| `threshold >= 2` with no officers | 400 | `INVALID_ARGUMENT` — `ErrNoOfficers` |
| Conflicting admin-credential inputs | 400 | `INVALID_ARGUMENT` |
| No admin-credential path selected | 400 | `INVALID_ARGUMENT` |
| Admin credential rejected | 400 | `INVALID_ARGUMENT` |
| CSR-sign/generate on a BYO-TLS node (no CA) | 409 | `FAILED_PRECONDITION` |
| Leader election or move during creation | — | waited out and retried; a record another node created first is re-read after replication |
| Any other first-boot failure | the underlying cause | returned, not retried — it is this node's own failure |
| No bootstrap source configured | 404 (`/v1/ca/bootstrap` unregistered) | — |

## Versioning

`/v1/config/initialized`, `/v1/ca/bootstrap` and `/v1/ceremony/initialize` are
under `/v1/`. Nothing here is deprecated.
