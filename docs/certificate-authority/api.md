# Certificate Authority — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-10

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

`POST /certs/revoke` — `{ "serial": "3f2a9c…" }`. Response:

```json
{ "status": "ok", "recordFound": true }
```

`recordFound` distinguishes a certificate this node holds a `CertRecord` for
(`true`) from a deny-by-serial revocation of one it does not (`false`) —
both are successes, reflected in `status`. gRPC and Unix carry the same
distinction in `RevokeCertResponse.record_found`; the RPC previously
returned `google.protobuf.Empty`.

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
| Serial not found — declared and mapped, currently **produced by nothing**: `RevokeCert` is deny-by-serial by design and never returns it | 404 | `NOT_FOUND` — `CertNotFoundError` |
| Export targets a revoked certificate | 409 | `FAILED_PRECONDITION` — `CertRevokedError` |
| Field validation failure | 400 | `INVALID_ARGUMENT` — `ValidationError` |
| Generation or export failure | 500 | `INTERNAL` — wraps the cause, `Unwrap()`-able |
| Missing permission | 403 | `PERMISSION_DENIED` |

`POST /certs/revoke` returns **200 / `OK` whether or not this node holds a
record for the serial**: the marker is written either way (deny-by-serial), so
both are successes. The response's `recordFound` field is how a caller tells
a mistyped serial apart from a deliberate revocation of a peer-issued one,
without either being reported as a failure — reporting a success as an error
would send a caller retrying and escalating over a revocation that is already
in force.

## Authenticating with a revoked certificate

Revocation is enforced on the **authentication path of every transport**, on
every request — not in a TLS handshake callback, which would leave a client
holding an established QUIC connection authorised indefinitely.

| Condition | REST | gRPC / Unix |
|---|---|---|
| Peer certificate is revoked | 401 | `UNAUTHENTICATED` — `auth.ErrCertificateRevoked` |
| Revocation status could not be read (store failure) | 503 | `UNAVAILABLE` — `auth.RevocationCheckError` |
| Barrier sealed, so there is no revocation state to read | request is admitted; reachable surface is confined to the sealed-window allowlist |

The 503 is the fail-closed case: the request is refused, and the status tells a
legitimate caller to retry rather than that its credential is bad. Admitting a
request whose revocation status is unknown would reinstate the defect
enforcement exists to remove.

A node caches a not-revoked answer for `auth.revocation_cache_ttl` (default
`30s`); **that is the window during which a freshly revoked certificate still
authenticates**. `0` disables caching. A revoked answer is cached without
expiry, because revocation here is a one-way door.

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
