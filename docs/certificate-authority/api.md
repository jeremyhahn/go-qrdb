# Certificate Authority — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

gRPC service `qrdb.v1.QRDBService`. The Unix socket transport serves the same
gRPC wire over a UDS, so its methods match the gRPC column.

## Capability matrix

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded (`Client.Certs()`) | `scp` permission |
|---|---|---|---|---|---|---|
| Generate client certificate | `POST /certs/generate` | `GenerateCert` | `cert.Generate` | `qrdb_cert_generate` | `GenerateCert` | `certs:generate` |
| Export PKCS#12 | `POST /certs/export` | `ExportCert` | `cert.Export` | `qrdb_cert_export` | `ExportCert` | `certs:export` |
| Revoke certificate | `POST /certs/revoke` | `RevokeCert` | `cert.Revoke` | `qrdb_cert_revoke` | `RevokeCert` | `certs:revoke` |
| List certificates | `GET /certs/list` | `ListCerts` | `cert.List` | `qrdb_cert_list` | `ListCerts` | `certs:list` |
| Fetch the CA bootstrap certificate | `GET /v1/ca/bootstrap` | MISSING | MISSING | MISSING | `CACertPEM()` in process | **public**, conditionally registered |

Four capabilities on all six transports, plus the public bootstrap route.

**Note the path prefix.** These are `/certs/...`, unversioned, even though the
CLI group is `qrdb ca`. The CLI was renamed (`certs` → `ca`); the routes were
not. `qrdb certs` remains as a redirect stub that only prints where to go.

**`ca info` and `ca bundle` have no distinct endpoint.** Both are CLI aliases
for `list`: CA-only metadata (DN, fingerprint, validity) and a standalone
CA+chain bundle export are **not exposed through the SDK**. Recorded here so
the missing capability is visible rather than inferred from a verb name.

## Schemas

`POST /certs/generate` — `GenerateRequest`:

```json
{ "username": "alice", "tenantID": "acme", "ttl": "8760h" }
```

`username` becomes the certificate **CN**; `tenantID` becomes the **OU**; `ttl`
zero or absent means the provider default (1 year).

Response — `CertBundle`:

```json
{ "certPEM": "<PEM>", "keyPEM": "<PEM>", "serial": "3f2a9c…" }
```

`GET /certs/list` — `tenantID` query parameter. Response elements are
`CertRecord`:

```json
{ "serial": "3f2a9c…", "username": "alice", "tenantID": "acme",
  "issuedAt": "2026-08-19T10:00:00Z", "expiresAt": "2027-08-19T10:00:00Z",
  "revoked": false }
```

`POST /certs/revoke` — `{ "serial": "3f2a9c…" }`.

`POST /certs/export` — certificate PEM, key PEM and a passphrase; returns the
PKCS#12 archive.

## Certificate properties

| Property | Value |
|---|---|
| Key algorithm | ECDSA P-256 (CA and client) |
| CA validity | 10 years |
| Default client validity | 1 year |
| Extended key usage | **ClientAuth only** |
| Serial | 128-bit random, hex |

## Barrier dependency

The CA private key is destroyed in memory when the barrier seals. While sealed:

| Operation | Behaviour |
|---|---|
| Generate, export, revoke, `IsRevoked`, list | `BarrierSealedError` |
| `CACertPEM()` / `GET /v1/ca/bootstrap` | **still works** — the CA certificate is public data served from cache |

That asymmetry is deliberate: a client needs the CA certificate to connect at
all, so refusing it while sealed would make a sealed server unreachable rather
than merely inert.

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success | 200 | `OK` |
| Barrier sealed | 503 | `UNAVAILABLE` — `BarrierSealedError` |
| CA key not loaded or destroyed | 503 | `UNAVAILABLE` — `CAKeyNotAvailableError` |
| Serial not found | 404 | `NOT_FOUND` — `CertNotFoundError` |
| Operation targets a revoked certificate | 409 | `FAILED_PRECONDITION` — `CertRevokedError` |
| Field validation failure | 400 | `INVALID_ARGUMENT` — `ValidationError` |
| Generation or export failure | 500 | `INTERNAL` — wraps the cause, `Unwrap()`-able |
| Missing permission | 403 | `PERMISSION_DENIED` |

## Revocation semantics

Revocation is an internal blocklist keyed by serial. **No CRL is published and
no OCSP responder is served.** Within this system the certificate stops
authenticating immediately; outside it, nothing observes the revocation. A
deployment with external relying parties should inject the go-xkms provider,
which adds CRL and OCSP — see [architecture.md](architecture.md).

## Pagination

`ListCerts` returns every certificate for the tenant and is not paginated.

## Versioning

Certificate routes are unversioned (`/certs/...`); the bootstrap route is under
`/v1/`. The `qrdb certs` **CLI group** is deprecated in favour of `qrdb ca`;
the REST paths were not renamed with it.
