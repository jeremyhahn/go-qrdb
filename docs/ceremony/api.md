# Initialization Ceremony — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-29

gRPC service `qrdb.v1.QRDBService`. The Unix socket transport serves the same
gRPC wire over a UDS, so its methods match the gRPC column.

## Capability matrix

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded (`Client.Ceremony()`) | `scp` permission |
|---|---|---|---|---|---|---|
| Initialize | `POST /v1/ceremony/initialize` | `CeremonyInitialize` | `ceremony.Initialize` | `qrdb_ceremony_initialize` | `CeremonyInitialize` | `ceremony:initialize` |
| Status | `GET /v1/ceremony/status` | `CeremonyStatus` | `ceremony.Status` | `qrdb_ceremony_status` | `CeremonyStatus` | `ceremony:status` |
| Sign CSR | `POST /v1/ceremony/sign-csr` | `CeremonySignCSR` | `ceremony.SignCSR` | `qrdb_ceremony_sign_csr` | `CeremonySignCSR` | `ceremony:sign_csr` |

Share holders — enrolment after initialization, claims, quorum rounds, rekey
and revocation — are the barrier holder routes in
[../shamir-custody/api.md](../shamir-custody/api.md). The ceremony has no
share-claim route of its own.

## Ceremony states

`CeremonyStatus` returns one of:

| State | Meaning |
|---|---|
| `awaiting_init` | freshly deployed, no ceremony performed |
| `enrolling` | M-of-N custody initialized; holders claiming their envelopes |
| `operational` | every initial holder has claimed, or single-admin mode |

## Schemas

`POST /v1/ceremony/initialize` — `CeremonyInitializeRequest`:

```json
{
  "so_pin": "…", "user_pin": "…",
  "threshold": 3, "policy": "designated",
  "officers": [
    { "username": "alice", "role": "so",        "csr_pem": "-----BEGIN CERTIFICATE REQUEST-----…" },
    { "username": "carol", "role": "custodian", "csr_pem": "…" }
  ],
  "hostname": "db.example.com", "sans": ["db.example.com"]
}
```

The SO and User PINs go straight to the configured `pin.PINBackend`; without
one the request is refused. `threshold >= 2` requires `officers` — every
initial share holder, officers and custodians alike, each with its role — and
fails closed with `ErrNoOfficers` without them. The unseal key is split
`threshold`-of-`len(officers)`. The caller may not name itself among them.

Response — `CeremonyInitializeResponse`: `spki_pin`, `ca_cert_pem`,
`tls_cert_pem`, `state`, `share_count`, and `holders` — each initial holder's
`username`, `role` and CA-issued `cert_pem`. **Never a share**: each holder
claims its own envelope.

`POST /v1/ceremony/sign-csr` — `{ "username", "so_pin", "csr_pem", "role" }`
issues a login certificate whose identity the CA assigns; the SO PIN is
verified by the PIN backend.

## Bootstrap-window routes

| Route | Auth | Purpose |
|---|---|---|
| `GET /v1/config/initialized` | **public** | has this system been initialized? |
| `GET /v1/ca/bootstrap` | **public**, registered only when a bootstrap cert is configured | fetch the CA bootstrap certificate |

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success | 200 | `OK` |
| `threshold >= 2` with no holders | 400 | `INVALID_ARGUMENT` — `ErrNoOfficers` |
| Threshold above the holder count | 400 | `INVALID_ARGUMENT` — `ErrInvalidThreshold` |
| Duplicate holder, self-enrolment, bad CSR or role | per [../shamir-custody/api.md](../shamir-custody/api.md#error-classes) | |
| Wrong SO PIN (sign-csr) | 401 | `UNAUTHENTICATED` |
| No PIN backend | 412 | `FAILED_PRECONDITION` |
| No `CeremonyCA` wired | 503 | `FAILED_PRECONDITION` |
| Ceremony already performed | 409 | `FAILED_PRECONDITION` — `ErrAlreadyInitialized` |
| Missing permission | 403 | `PERMISSION_DENIED` |

## Versioning

Ceremony REST paths are under `/v1/`. The ceremony's former share-claim,
certificate-claim, custodian enrolment and unseal-share routes were removed;
see the CHANGELOG. The CLI's `qrdb ceremony init` verb was removed in favour
of the root `qrdb init`.
