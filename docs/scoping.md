<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Tenant & Database Scoping

Go-qrdb scopes every request to a tenant and a database. The **tenant is derived
from the authenticated identity** (the mTLS client certificate / session), and the
**database is selected by the `X-QRDB-Database` header** (or the SDK's scoped
client). This lets a single QRDB cluster serve many tenants and many logical
databases per tenant from one endpoint, while the tenant boundary is enforced
cryptographically by who you authenticate as — not by a client-supplied value.

## Tenant Scope (from the authenticated identity)

The request's tenant is the `TenantID` of the authenticated principal. The shared
auth middleware binds it into the request context (`tenant.WithTenantID`) right
after authentication, so every downstream service — KV, watch, lease, index,
time-series, and vector — operates within that tenant.

| Aspect | Details |
|--------|---------|
| **Source** | The authenticated mTLS identity's `TenantID` (never a client header) |
| **Default** | An identity with no tenant resolves to the structurally privileged `system` tenant |
| **Scope** | All KV, watch, lease, index, time-series, and vector operations |
| **Cross-tenant** | Not selectable per-request. To act as another tenant, authenticate as an identity in that tenant. Administrative cross-tenant operations (user/role/cert management) take an explicit `tenantID` parameter and are gated by RBAC `ScopeCross` (system-tenant identities only). |

> **Security note:** there is no `X-QRDB-Tenant` header. It was removed because it
> was read before authentication and was not bound to the caller's identity — a
> tenant-spoofing vector. The tenant is now authoritative from the certificate.

## X-QRDB-Database

Specifies the database name within the tenant's shard. If omitted, defaults to `default`.

```
X-QRDB-Database: user-data
```

| Aspect | Details |
|--------|---------|
| **Required** | No (defaults to `default` database) |
| **Source** | `X-QRDB-Database` header, or the SDK scoped client |
| **Example Values** | `default`, `users`, `analytics`, `logs` |
| **Scope** | All KV, transaction, batch, and query operations |
| **Invalid Database** | Returns `404 Not Found` |

## Example Usage

### REST API

```bash
# Read from the "users" database. The tenant is whatever acme-admin.pem
# authenticates as — it is NOT specified in the request.
curl -X GET "https://localhost:8443/v1/kv/users/123" \
  --cert acme-admin.pem --key acme-admin-key.pem \
  -H "X-QRDB-Database: users"

# Operating as a different tenant means authenticating with that tenant's cert:
curl -X GET "https://localhost:8443/v1/kv/setting" \
  --cert system-admin.pem --key system-admin-key.pem   # → system tenant
```

### SDK Usage

```go
client, _ := sdk.NewClient(transport)
client.Connect(ctx)

// Tenant is fixed by the credentials the transport authenticated with.
// The scoped client selects the DATABASE within that tenant.
kv := client.KV()                       // default database
kvUsers, _ := client.DatabaseScoped("users")
val, _ := kvUsers.Get(ctx, "mykey")
```

The SDK selects the database scope; the tenant is determined by the authenticated
identity and cannot be overridden by the client.

## Keyspace Isolation

Isolation holds regardless of the database selected:

1. **Tenant Prefix** — Keys are prefixed with the authenticated tenant ID (e.g., `acme-corp:key`)
2. **Database Prefix** — Within the tenant, keys are further scoped to the database (e.g., `acme-corp:users:key`)
3. **Vector Namespace Tenant-Partitioning** — Vector namespace names are scoped by the authenticated tenant ID
   (e.g., tenant-a's `embeddings` namespace is stored separately from tenant-b's `embeddings`),
   preventing namespace-name collision or cross-tenant access
4. **Crypto Isolation** (optional) — If the tenant uses `IsolationBarrier`, an additional per-tenant DEK encrypts that tenant's KV data.
   Vector data is encrypted at rest under the cluster `StorageBarrier`; its tenant isolation comes from the namespace partitioning in step 3, not a per-tenant vector DEK

Tenant and namespace names are validated to exclude control bytes (including the
internal `\x1f` namespace-scoping separator), preventing key-injection attacks.

## Tenant Isolation Modes

QRDB supports two tenant isolation postures, selected via `tenant.strict_isolation`
in the YAML configuration (or forced to strict by a FIPS-140 build):

### Standard Mode (`strict_isolation: false`, the default)

- Un-tenanted identities — those whose mTLS certificate carries no `TenantID` — are
  silently assigned to the structurally privileged `system` tenant.
- Vector namespace creation is **not refused** for system-tenant identities. All
  un-owned namespaces pool into `system`.
- **This is NOT cross-tenant isolation.** Standard mode is a single-tenant convenience
  for local development, single-operator deployments, or trusted internal tools where
  namespace ownership is not a security boundary.
- A startup `WARN` log is emitted when standard mode is active so operators are never
  silently running in the weaker posture.

### Strict Mode (`strict_isolation: true`, or any FIPS-140 build)

- Vector namespace creation is **refused** (`403 Forbidden`, `tenant required`) when
  the authenticated identity resolves to the `system` tenant, preventing accidental
  un-owned namespace creation.
- The tenant comes entirely from the authenticated mTLS identity; there is no implicit
  fallback.
- Required for multi-tenant SaaS, HIPAA, PCI-DSS, or any deployment where namespace
  ownership must be enforced per authenticated identity.
- Cannot be relaxed when the binary is compiled with FIPS-140 (`GOFIPS140=latest`
  or `GODEBUG=fips140=on`).

See [12-configuration.md](12-configuration.md) for the `tenant.strict_isolation` YAML
field reference.

## Error Cases

| Condition | HTTP Status | Error Message |
|-----------|-------------|---------------|
| Database does not exist | 404 | `database not found` |
| Tenant isolation mode mismatch | 400 | `tenant isolation type changed` (if changing isolation type with existing data) |
| Strict isolation, identity has no tenant | 403 | `tenant required` (vector namespace creation in strict mode) |
| Database header missing (default used) | 200 | (No error, uses `default`) |

## Multi-Database Patterns

A tenant (authenticated identity) can use multiple logical databases for different
workloads, selected via the database scope:

```
# Same tenant (one identity), different databases:
X-QRDB-Database: users        # user service
X-QRDB-Database: analytics    # analytics service
X-QRDB-Database: prod         # production data
```

Each database can have independent secondary indexes, retention policies, and backup
schedules. Multi-tenant SaaS isolation is achieved by issuing each customer an
identity in their own tenant — the tenant boundary is then enforced by authentication,
not by a request parameter.

## See Also

- [databases/README.md](databases/README.md) — Database management and lifecycle
- [seal/multi-tenant.md](seal/multi-tenant.md) — Tenant encryption isolation
- [api.md](api.md) — REST and gRPC API reference
