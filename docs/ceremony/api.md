# Initialization Ceremony — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-03

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
    {
      "username": "carol", "role": "custodian", "csr_pem": "…",
      "encryption_kem": "dhkem-x25519", "encryption_key": "yJRYgAtmk1A…",
      "factors": [{ "name": "fido2", "params": { "attestation": "…" } }],
      "designated": "fido2"
    }
  ],
  "hostname": "db.example.com", "sans": ["db.example.com"],
  "admin_csr_pem": "…", "admin_cert_pem": "…", "admin_cert_pool_pem": "…",
  "admin_generate": false,
  "secret": "YmFycmllci1wYXNzcGhyYXNl"
}
```

The SO and User PINs go straight to the configured `pin.PINBackend`; without
one the request is refused. `threshold >= 2` requires `officers` — every
initial share holder, officers and custodians alike, each with its role — and
fails closed with `ErrNoOfficers` without them. The unseal key is split
`threshold`-of-`len(officers)`. The caller may not name itself among them.

**Each officer carries the same fields `POST /v1/barrier/officers`
(`OfficerRegisterRequest`, [../shamir-custody/api.md](../shamir-custody/api.md))
does**: `Initialize` is the ONLY system
bootstrap, so it is also the only place Shamir custody over the system
barrier is ever established, and a holder who needs any of the fields below
has no other registration path to get them through. `encryption_kem`/
`encryption_key` name an optional separate share-envelope recipient key
(HPKE suite, base64 public key); without one the certificate key receives
the envelope. `factors` are additional authentication factors to enrol
(`mtls` is always enrolled, from the issued certificate). `designated`
names the factor fixed at enrolment under the designated factor policy;
`mtls` when empty. `Initialize` registers every officer through the
identical holder-engine call `BarrierOfficerRegister` makes — there is no
second registration path.

**`secret` is `bytes`, base64-encoded on the wire** (**BREAKING**: it was a
plain string before; see CHANGELOG) -- a Go string is immutable and can
never be wiped from memory after use the way a `[]byte` can, matching
`BarrierInitializeRequest.secret`'s existing convention. It is the system
barrier's passphrase for the single-admin path (`threshold < 2`); omit it for
`--barrier-strategy none` and for M-of-N, where the barrier stays sealed
until officers reconstruct it from their shares. The MCP tool's `secret`
argument is the one exception: it is plain text there, not base64, matching
its `passphrase` argument's convention on the barrier and tenant-barrier
tools.

**`admin_csr_pem`, `admin_cert_pem`/`admin_cert_pool_pem`, and
`admin_generate` select the bootstrap admin's credential path**: CSR-sign,
BYOC, or generate — dev/test/evaluation only, not suitable for production.
Exactly one must be set; a request selecting none is refused
(`*ceremony.NoAdminCredentialSelectedError`, 400), and more than one is
refused too (`*ceremony.ConflictingAdminCredentialInputsError`, 400).

Response — `CeremonyInitializeResponse`: `spki_pin`, `ca_cert_pem`,
`tls_cert_pem`, `state`, `share_count`, `holders` — each initial holder's
`username`, `role` and CA-issued `cert_pem` (**never a share**: each holder
claims its own envelope) — and the bootstrap admin's credential result:
`admin_cert_pem`, `admin_credential_source` (`"csr"`, `"byoc"` or
`"generated"`), and `admin_key_pem`.

```json
{ "spki_pin": "…", "ca_cert_pem": "…", "state": "operational",
  "admin_cert_pem": "…", "admin_credential_source": "generated",
  "admin_key_pem": "LS0tLS1CRUdJTiBFQyBQUklWQVRFIEtFWS0tLS0tCuKApg==" }
```

**`admin_key_pem` is `bytes`, base64-encoded on the wire** (**BREAKING**: it
was a plain string before; see CHANGELOG) -- the PEM text re-encoded as
base64 by the JSON marshaler: decode it to recover the PEM. It is present
**only** when `admin_credential_source` is
`"generated"`. It is returned exactly once; the server retains no copy and
wipes its own in-memory copy once the response has been sent, on every
transport. First boot binds `admin_cert_pem`'s thumbprint to the admin
account once that account exists — see
[../firstboot/api.md](../firstboot/api.md).

`POST /v1/ceremony/sign-csr` — `{ "username", "so_pin", "csr_pem", "role" }`
issues a HOLDER certificate only (`role` is `so` or `custodian`, which
carries no roles) whose identity the CA assigns; the SO PIN is verified by
the PIN backend. A login certificate for a user-facing account (admin,
operator, user, auditor) is a SEPARATE path: the tenant Admin issues it
through the certs CSR-sign or BYOC flow
([../certificate-authority/api.md](../certificate-authority/api.md)), which
requires an authenticated, authorized caller rather than only the SO PIN.
`sign-csr` refuses any other role with `ceremony.ErrInvalidRole`.

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
