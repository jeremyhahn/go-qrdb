# Trust Anchors — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-03

Register, list and remove a tenant's trust anchors on every transport.

gRPC service `qrdb.v1.QRDBService`. The Unix socket transport serves the same
gRPC wire over a UDS, so its methods match the gRPC column.

## Capability matrix

| Capability | REST | gRPC / Unix | QUIC op | MCP tool | Go SDK (`TenantClient`) | Permission |
|---|---|---|---|---|---|---|
| Register | `POST /v1/tenants/{id}/trust-anchors` | `TrustAnchorAdd` | `tenant.TrustAnchorAdd` | `qrdb_trust_anchor_add` | `AddTrustAnchors` | `trust_anchors:add` |
| List | `GET /v1/tenants/{id}/trust-anchors` | `TrustAnchorList` | `tenant.TrustAnchorList` | `qrdb_trust_anchor_list` | `ListTrustAnchors` | `trust_anchors:list` |
| Remove | `DELETE /v1/tenants/{id}/trust-anchors/{fingerprint}` | `TrustAnchorRemove` | `tenant.TrustAnchorRemove` | `qrdb_trust_anchor_remove` | `RemoveTrustAnchor` | `trust_anchors:remove` |

The Go SDK methods are the same on all six SDK transports (REST, gRPC, Unix,
QUIC, MCP, embedded). Anchors can also be registered in the tenant-create
request itself (`trust_anchors_pem`); see
[multi-tenancy/api.md](../multi-tenancy/api.md).

A node built without a certificate provider does not serve these verbs: REST
does not register the routes, and the other transports answer
`Unimplemented`.

## Authorization: your own tenant only

The three permissions are `ScopeSelfStrict`. The tenant named in the request
must be the caller's own; a system-tenant operator cannot manage another
tenant's anchors, and a tenant's Admin cannot manage any tenant but its own.
The refusal is 403 on REST, `PERMISSION_DENIED` on gRPC and Unix, the
permission-denied status on QUIC and MCP, and `*tenant.CrossTenantDeniedError`
on the embedded transport.

## Schemas

### Register

```json
POST /v1/tenants/acme/trust-anchors
{ "certs_pem": "-----BEGIN CERTIFICATE-----\n..." }
```

`certs_pem` is a PEM bundle of one or more CA certificates. Every certificate
is validated before any is stored; one invalid certificate refuses the whole
request. Response `201 Created`, the bundle's anchors as registered (an anchor
the tenant already had keeps its original `added_at`):

```json
{
  "anchors": [
    {
      "fingerprint": "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
      "subject": "CN=Acme Issuing CA,O=Acme",
      "not_before": "2026-01-01T00:00:00Z",
      "not_after": "2031-01-01T00:00:00Z",
      "cert_pem": "-----BEGIN CERTIFICATE-----\n...",
      "added_at": "2026-10-03T12:00:00Z"
    }
  ]
}
```

`added_at` is omitted when an injected trust store does not record it. On
gRPC the timestamps are RFC 3339 strings in `TrustAnchorListResponse`.

### List

`GET /v1/tenants/acme/trust-anchors` returns the same `{"anchors": [...]}`
shape, `200 OK`. A tenant with no anchors returns an empty list.

### Remove

`DELETE /v1/tenants/acme/trust-anchors/{fingerprint}` returns `204 No Content`
(gRPC: `google.protobuf.Empty`). The fingerprint is the lowercase hex SHA-256
of the certificate's DER encoding, as listed.

## Validation

Every certificate in a bundle must:

- be a CA certificate (basic constraints present, `CA` true);
- permit certificate signing, when it declares a key usage at all;
- be inside its validity window at the time of the request.

Duplicates inside a bundle are collapsed, and re-adding an anchor the tenant
already has is not an error. A tenant holds at most **32** anchors.

## Errors

| Condition | Error | REST | gRPC |
|---|---|---|---|
| Empty bundle, or no certificate in it | `*trustanchor.EmptyBundleError` | 400 | `INVALID_ARGUMENT` |
| A certificate cannot be an anchor | `*trustanchor.InvalidAnchorError` (its index and reason) | 400 | `INVALID_ARGUMENT` |
| Over the per-tenant limit | `*trustanchor.TooManyAnchorsError` | 429 | `RESOURCE_EXHAUSTED` |
| Unknown fingerprint on remove | `*trustanchor.AnchorNotFoundError` | 404 | `NOT_FOUND` |
| The store is managed outside qrdb | `*trustanchor.ExternallyManagedError` | 409 | `FAILED_PRECONDITION` |
| Another tenant named | `*tenant.CrossTenantDeniedError` | 403 | `PERMISSION_DENIED` |

The cross-transport mapping is one table (`pkg/api/errclass`), so QUIC, MCP
and the SDKs classify these errors the same way.

## Sealed tenants

The built-in store keeps anchors in the replicated system store, inside the
tenant's identity namespace, not behind the tenant's own barrier. A tenant
whose barrier is sealed can still list and manage its anchors, and an admin
whose certificate chains to one can still log in to unseal it.
