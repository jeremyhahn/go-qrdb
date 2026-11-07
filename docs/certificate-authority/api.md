# Certificate Authority — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-26
> Conflicts with this document are defects. Fix them here first.

gRPC service `qrdb.v1.QRDBService`. The Unix socket transport serves the same
gRPC wire over a UDS, so its methods match the gRPC column.

## Capability matrix

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded (`Client.Certs()`) | `scp` permission |
|---|---|---|---|---|---|---|
| Issue a client certificate from a CSR | `POST /certs/issue` | `IssueCert` | `cert.Issue` | `qrdb_cert_issue` | `IssueCert` | `certs:issue` |
| Enrol a bring-your-own certificate | `POST /certs/enroll` | `EnrollCert` | `cert.Enroll` | `qrdb_cert_enroll` | `EnrollCert` | `certs:enroll` |
| Issue a delegation to a service principal | `POST /certs/delegate` | `IssueDelegation` | `cert.Delegate` | `qrdb_cert_delegate` | `IssueDelegation` | `certs:delegate` |
| Write a standing delegation policy | `POST /certs/delegate/policies` | `PutDelegationPolicy` | `cert.DelegationPolicyPut` | `qrdb_cert_delegation_policy_put` | `PutDelegationPolicy` | `certs:delegate` |
| List standing delegation policies | `GET /certs/delegate/policies` | `ListDelegationPolicies` | `cert.DelegationPolicyList` | `qrdb_cert_delegation_policy_list` | `ListDelegationPolicies` | `certs:delegate` |
| Delete a standing delegation policy | `DELETE /certs/delegate/policies/{principal}` | `DeleteDelegationPolicy` | `cert.DelegationPolicyDelete` | `qrdb_cert_delegation_policy_delete` | `DeleteDelegationPolicy` | `certs:delegate` |
| Revoke certificate | `POST /certs/revoke` | `RevokeCert` | `cert.Revoke` | `qrdb_cert_revoke` | `RevokeCert` | `certs:revoke` |
| List certificates | `GET /certs/list` | `ListCerts` | `cert.List` | `qrdb_cert_list` | `ListCerts` | `certs:list` |
| Fetch the CA bootstrap certificate | `GET /v1/ca/bootstrap` | MISSING | MISSING | MISSING | MISSING | **public**, conditionally registered |

Eight capabilities on all six transports, plus the public bootstrap route.
PKCS#12 export is not a server capability: `qrdb ca export-bundle` builds the
archive locally ([cli.md](cli.md#qrdb-ca-export-bundle)).

**No verb generates a client's private key.** Issue and delegate sign a
certificate signing request the requester made with its own key; enrol binds a
certificate the requester already holds. Both act on an **existing** account of the
caller's own tenant and bind the certificate's public key to it in the same
call. The trust anchors that enrolment verifies against are their own feature:
[../trust-anchors/api.md](../trust-anchors/api.md).


### The permission column is only half the authorization

The `scp` permission above is checked against the **caller's own tenant**. It
does not say *whose* certificates the call may reach, and on this surface those
are separate questions — one process-wide certificate provider serves every
tenant. A second check establishes the target:

| Capability | Target tenant | Additional permission |
|---|---|---|
| Issue, Enrol | the **caller's own** tenant — no request field names a tenant | — (`certs:issue` / `certs:enroll` re-checked against it) |
| Delegate | the **caller's own** tenant, never the system tenant — no request field names a tenant | — (`certs:delegate` re-checked against it) |
| Delegation policy put, list, delete | the **caller's own** tenant, never the system tenant, never a delegated caller — no request field names a tenant | — (`certs:delegate` re-checked against it) |
| List | the request's `tenantID` / `tenant_id` | — (`certs:list` re-checked against the target) |
| Revoke, serial **known** to this node | the owning tenant, resolved from the serial | — (`certs:revoke` re-checked against the owner) |
| Revoke, serial **unknown** to this node | none — deny-by-serial | **`certs:revoke:cross`** (system tenant only) |

A caller naming another tenant is refused — **every** caller, the system tenant included: the `certs:*`
permissions are `ScopeSelfStrict`, which the system-tenant rule does not lift.
The refusal is 403 on REST, `PERMISSION_DENIED` on gRPC and the unix socket,
`StatusPermissionDenied` on QUIC, the permission-denied tool code on MCP, and
`*tenant.CrossTenantDeniedError` on the embedded transport. See
[architecture.md](architecture.md#which-tenant-a-certs-call-may-act-on) for why
deny-by-serial is a CA-operator capability.

`tenant_id` on the MCP `qrdb_cert_list` tool defaults to the **system tenant**
when omitted; that default is gated exactly like an explicit cross-tenant
request, so a tenant caller names its own tenant.

**`ca info` and `ca bundle` have no distinct endpoint.** Both are CLI aliases
for `list`: CA-only metadata (DN, fingerprint, validity) and a standalone
CA+chain bundle export are **not exposed through the SDK**.

## Schemas

The REST certificate bodies use camelCase field names; gRPC, QUIC and MCP use
the snake_case names in `qrdb.proto` (`csr_pem`, `cert_pem`, `chain_pem`,
`user_id`, `tenant_id`, `not_after`).

`POST /certs/issue`:

```json
{ "username": "alice", "csrPEM": "<PEM CERTIFICATE REQUEST>", "ttl": "8760h" }
```

`username` names an account that must already exist in the caller's tenant; it
becomes the certificate **CN**, and the caller's tenant becomes the **OU**.
Only the CSR's public key is used — its Subject and any SANs it asks for are
ignored. The CSR's signature must verify, and its key must be ECDSA P-256,
P-384 or P-521, RSA of at least 2048 bits, or Ed25519. `ttl` is a Go duration;
absent means the provider default (1 year). The certificate is signed by the
tenant's own login CA and its SPKI thumbprint is bound to the account; if the
binding fails, the certificate is revoked before the error is returned.

`POST /certs/enroll`:

```json
{ "username": "alice", "certPEM": "<PEM, leaf first>", "chainPEM": "<PEM intermediates>" }
```

The leaf must chain to one of the tenant's registered trust anchors for client
authentication, its CN must equal `username`, and the tenant it authenticates
as (read by the mTLS authenticator's own rule) must be the caller's tenant.
`chainPEM` is optional; any certificates after the leaf in `certPEM` are
offered as intermediates too.

Both return `201` with the bound certificate — never a key:

```json
{ "userID": "42", "username": "alice", "tenantID": "acme",
  "certPEM": "<PEM>", "serial": "3f2a9c…", "thumbprint": "<SPKI SHA-256>",
  "notAfter": "2027-10-03T10:00:00Z" }
```

`serial` is omitted for an enrolled certificate, which this CA did not issue.

## Issuing a delegation

`POST /certs/delegate` has the caller's own tenant's login CA issue a
short-lived client certificate to an embedder's **service principal**, naming
the permissions the principal may exercise inside the tenant. How the
certificate is verified, and what authority it confers, is in
[../identity-rbac/api.md](../identity-rbac/api.md#delegation-certificates).

```json
{ "principal": "billing", "scope": ["kv:get", "kv:put"],
  "csrPEM": "<PEM CERTIFICATE REQUEST>", "ttl": "30m" }
```

| Field | Rule |
|---|---|
| `principal` | `[a-z0-9][a-z0-9._-]*`, at most 64 bytes, and not the name of a built-in role |
| `scope` | 1 to 64 registered `resource:action` permissions, no duplicates |
| `csrPEM` | the principal's own CSR; only its public key is used, under the same key rules as `POST /certs/issue` |
| `ttl` | a Go duration; absent means 1 hour, more than `24h` is refused |

The certificate's CN is `qrdb-service:<principal>`, its OU is the caller's
tenant, and its URI SAN is `spiffe://<tenant>/svc/<principal>`, under the
tenant's own trust domain. The grant rides
in a **critical** extension, so a verifier that does not understand it refuses
the certificate rather than reading it as a user login. EKU is ClientAuth only.

Only a tenant delegates. The system tenant is refused, and so is a caller
that is itself authenticated by a delegation: a delegation that could renew
itself would have no lifetime. The one exception is a renewal within a
standing policy ([below](#standing-delegation-policies)). Every issuance is written to the tenant's audit
trail as `cert.delegate` with the principal and scope, before the CA signs.

Returns `201` with the certificate — never a key:

```json
{ "tenantID": "acme", "principal": "billing", "scope": ["kv:get", "kv:put"],
  "certPEM": "<PEM>", "serial": "3f2a9c…", "notAfter": "2026-10-09T10:30:00Z" }
```

gRPC and Unix use `IssueDelegationRequest` / `IssuedDelegationResponse` with
`principal`, `scope`, `csr_pem`, `ttl` and `tenant_id`, `cert_pem`, `serial`,
`not_after` (RFC 3339). QUIC takes and returns the REST bodies; the MCP tool
takes `principal`, `scope`, `csr_pem` and `ttl`.

A delegation is revoked like any certificate this CA issued, by serial with
`POST /certs/revoke`, and appears in `GET /certs/list`. It is refused within
`auth.revocation_cache_ttl` of revocation, and outright at `notAfter`.

## Standing delegation policies

A standing policy is the tenant's grant written down once, so a delegation can
be minted and renewed with no tenant admin at hand. One policy stands per
(tenant, principal), stored under `__sys:cert_delegation_policy/`.

`POST /certs/delegate/policies` writes or replaces the policy for a principal
in the caller's own tenant:

```json
{ "principal": "billing", "scope": ["kv:get", "kv:put"], "ttl": "1h" }
```

It returns `201` with `{ "tenantID", "principal", "scope", "ttl" }`. `ttl` is
the longest lifetime a certificate minted under it may have, at most 24 hours.
`GET /certs/delegate/policies` returns `{ "policies": [...] }`, each element
that same shape. `DELETE /certs/delegate/policies/{principal}` returns `204`.

All three are `certs:delegate` in the caller's own tenant; the system tenant
and a delegated caller are refused (`service.DelegationDeniedError`). The put
and the delete are written to the tenant's audit trail as
`cert.delegation_policy.put` and `cert.delegation_policy.delete`.

gRPC and Unix use `PutDelegationPolicy`, `ListDelegationPolicies` and
`DeleteDelegationPolicy`; QUIC uses `cert.DelegationPolicyPut`,
`cert.DelegationPolicyList` and `cert.DelegationPolicyDelete` with the REST
bodies; the MCP tools are `qrdb_cert_delegation_policy_put`,
`qrdb_cert_delegation_policy_list` and `qrdb_cert_delegation_policy_delete`;
the Go SDK has `PutDelegationPolicy`, `ListDelegationPolicies` and
`DeleteDelegationPolicy(principal)`.

**Minting and renewal.** The embedding host mints the first certificate in
process with `Node.MintDelegation`, as its service principal; it is audited as
`cert.delegation.mint`. Before it expires, the delegation renews itself: a
caller authenticated by a delegation may call `IssueDelegation` for its own
principal while a policy stands for it. The request is bounded by the policy
(`Policy.Bound`): it may narrow the scope and shorten the lifetime, never widen
either.

**Revocation.** Deleting the policy stops every further mint and renewal: each
is then refused with `service.DelegationPolicyNotFoundError` (404,
`NOT_FOUND`). A certificate already minted lives until it expires or is
revoked by serial.

`GET /certs/list` — `tenantID` query parameter, optional; empty means the
caller's own tenant, the only one `certs:list` admits. The response is
`{ "certs": [...] }`, each element a `CertRecord`:

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
distinction in `RevokeCertResponse.record_found`.

## Certificate properties

| Property | Value |
|---|---|
| CA key algorithm | ECDSA P-256 |
| Client key | the requester's own: ECDSA P-256/384/521, RSA ≥ 2048 bits, or Ed25519 |
| CA validity | 10 years |
| Default client validity | 1 year |
| Extended key usage | **ClientAuth only** |
| Serial | 128-bit random, hex |

## Barrier dependency

The CA private key is destroyed in memory when the barrier seals. While sealed:

| Operation | Behaviour |
|---|---|
| Issue, delegate, revoke, `IsRevoked`, list | `BarrierSealedError` |
| `GET /v1/ca/bootstrap` | **still works** — the CA certificate is public data served from cache |

That asymmetry is deliberate: a client needs the CA certificate to connect at
all, so refusing it while sealed would make a sealed server unreachable rather
than merely inert.

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success | 200 (201 for issue and enrol) | `OK` |
| Barrier sealed | 503 | `UNAVAILABLE` — `BarrierSealedError` |
| CA key not loaded or destroyed | 503 | `UNAVAILABLE` — `CAKeyNotAvailableError` |
| Serial not found — declared and mapped, **produced by nothing**: `RevokeCert` is deny-by-serial by design and never returns it | 404 | `NOT_FOUND` — `CertNotFoundError` |
| Field validation failure | 400 | `INVALID_ARGUMENT` — `ValidationError`, `service.CertEnrollmentFieldError` |
| CSR does not parse, its signature does not verify, or its key is not accepted | 400 | `INVALID_ARGUMENT` — `cert.InvalidCSRError` |
| Delegation names a malformed or built-in-role principal, an unregistered or duplicate permission, or a lifetime over 24 hours | 400 | `INVALID_ARGUMENT` — `service.DelegationRequestError` |
| Delegation requested by the system tenant, or by a delegated caller outside a standing policy | 403 | `PERMISSION_DENIED` — `service.DelegationDeniedError` |
| Delegation policy written, listed or deleted by the system tenant or by a delegated caller | 403 | `PERMISSION_DENIED` — `service.DelegationDeniedError` |
| A mint, renewal or policy delete for a principal no standing policy names | 404 | `NOT_FOUND` — `service.DelegationPolicyNotFoundError` |
| The named account does not exist in the caller's tenant | 404 | `NOT_FOUND` — `user.UserNotFoundError` |
| Enrolled certificate does not chain to a trust anchor of the tenant | 400 | `INVALID_ARGUMENT` — `trustanchor.CertificateNotTrustedError` |
| Enrolled certificate authenticates as another account or tenant | 400 | `INVALID_ARGUMENT` — `certenroll.IdentityMismatchError` |
| Enrolment attempted with no trust anchor registered | 409 | `FAILED_PRECONDITION` — `trustanchor.NoAnchorsError` |
| Issue or enrol not configured on this node | 501 | `UNIMPLEMENTED` — `service.CertEnrollmentNotConfiguredError` |
| Revoke or list with no certificate provider on this node | 501 — `ServiceNotImplementedError` | `UNIMPLEMENTED` — `ServiceNotConfiguredError` |
| Signing failure | 500 | `INTERNAL` — wraps the cause, `Unwrap()`-able |
| Missing permission | 403 | `PERMISSION_DENIED` |
| Target tenant is not the caller's — for every caller, the system tenant included | 403 | `PERMISSION_DENIED` |
| Deny-by-serial attempted without `certs:revoke:cross` | 403 | `PERMISSION_DENIED` |
| Owning tenant could not be resolved (no provider wired, store unreachable) | 503 | `UNAVAILABLE` — the decision was never made, so 403 would be a lie |

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

## Authenticating with the wrong tenant's CA (issuer-tenant binding)

Every tenant's Key B CA is trusted on the SAME client-facing listener (see
[architecture.md](architecture.md#issuer-tenant-binding)), so a verified
chain alone is not enough: the issuer-tenant binding additionally requires
that chain's issuer be either the cluster CA (Key A, any tenant) or the
Key B CA registered for the tenant the leaf's own Subject names — never a
*foreign* tenant's Key B CA. This is enforced on the authentication path of
every transport, the same layer revocation is, and separately on the
on-behalf forwarding path (`AuthMiddleware.authenticateOnBehalf`, which
never runs the authenticator chain so does not inherit the first check).

| Condition | REST | gRPC / Unix |
|---|---|---|
| Verified chain's issuer is not tenant X's registered CA, for a leaf claiming tenant X | 401 | `UNAUTHENTICATED` — `*auth.IssuerTenantMismatchError` |
| The issuer-tenant lookup itself failed (barrier sealed, store unreachable) | 401 | `UNAUTHENTICATED` — `*auth.IssuerTenantMismatchError` wrapping the cause |
| On-behalf forwarding peer's issuer is not the cluster CA | 401 | `UNAUTHENTICATED` |

`IssuerTenantMismatchError` matches `auth.ErrInvalidCertificate` under
`errors.Is`/`errors.As`, so every transport classifies it exactly as a
malformed or untrusted certificate — a caller sees the same error shape a
revoked or otherwise invalid certificate produces, not a distinct status
that would let a prober distinguish "right CA, wrong tenant" from "no CA
trusts this at all."

An injected `cakey.ProviderFactory` (`WithKeyProviderFactory` /
`WithCertKeyProviderFactory`) is checked the same way, resolved per tenant
(`factory.ProviderForTenant(tenantID)`) — never a single shared provider
serving every tenant, which would make this binding vacuous (every
tenant's certificate would verify against the SAME injected CA). See
[architecture.md](architecture.md#the-ca-key-provider-seam-pkgcakey) for
the full rationale.

## Revocation semantics

Revocation is an internal blocklist keyed by serial. **No CRL is published and
no OCSP responder is served.** Within this system the certificate stops
authenticating within `auth.revocation_cache_ttl`; outside it, nothing observes
the revocation. A
deployment with external relying parties should inject the go-xkms provider,
which adds CRL and OCSP — see [architecture.md](architecture.md).

## Pagination

`ListCerts` returns every certificate for the tenant and is not paginated.

## Versioning

Certificate routes are unversioned (`/certs/...`); the bootstrap route is under
`/v1/`.
