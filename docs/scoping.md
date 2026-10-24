# Tenant & Database Scoping

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-23
> Companion reference; the feature folders under `docs/` are the contract and win on any disagreement.

<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

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
| **"Is this system scope?"** | Use `tenantid.IsSystem(id string) bool` (or `rbac.IsSystemTenant(identity *auth.Identity) bool` for an `*auth.Identity`) — the canonical predicate, re-exported through `sdk/go` as `IsSystemTenant` / `IdentityIsSystemTenant`. Never re-derive `id == "" \|\| id == tenantid.System` at a call site. |
| **Cross-tenant** | Not selectable per-request. To act as another tenant, authenticate as an identity in that tenant. Administrative operations that name a tenant (user, role and certificate management) take an explicit `tenantID` parameter, and the permission is re-checked against the named tenant (`AuthMiddleware.AuthorizeTenantTarget`): a tenant may act on itself, and only a system-tenant identity on another tenant. |

There is no `X-QRDB-Tenant` header: a client-supplied tenant is never read.

## X-QRDB-Database

Specifies the database name within the tenant's shard. If omitted, defaults to the
implicit bootstrap database `system` (`tenant.DefaultDatabase`). Selecting `system`
explicitly is equivalent to supplying no database scope.

```
X-QRDB-Database: user-data
```

| Aspect | Details |
|--------|---------|
| **Required** | No (defaults to the `system` database) |
| **Source** | `X-QRDB-Database` header, or the SDK scoped client |
| **Example Values** | `users`, `analytics`, `logs` |
| **Scope** | All KV, transaction, batch, and query operations |
| **Invalid Database** | Returns `404 Not Found` |

## Example Usage

### REST API

```bash
# Read from the "users" database. The tenant is whatever acme-admin.pem
# authenticates as — it is NOT specified in the request.
curl -X GET "https://localhost:63002/kv/users/123" \
  --cert acme-admin.pem --key acme-admin-key.pem \
  -H "X-QRDB-Database: users"

# Operating as a different tenant means authenticating with that tenant's cert:
curl -X GET "https://localhost:63002/kv/setting" \
  --cert system-admin.pem --key system-admin-key.pem   # → system tenant
```

### SDK Usage

```go
client, _ := sdk.NewClient(transport)
client.Connect(ctx)

// Tenant is fixed by the credentials the transport authenticated with.
// The scoped client selects the DATABASE within that tenant.
kv := client.KV()                       // system (unscoped) database
users, _ := client.DatabaseScoped("users") // a *Client scoped to "users"
val, _ := users.KV().Get(ctx, "mykey")
```

The SDK selects the database scope; the tenant is determined by the authenticated
identity and cannot be overridden by the client.

## Keyspace Isolation

Isolation holds regardless of the database selected:

1. **Tenant and Database Prefix** — Keys are prefixed with `<tenant>/<database>/` (`tenant.ComposeNamespace`), e.g. `acme-corp/users/key`; an empty tenant or database falls back to `system`
2. **Vector Namespace Tenant-Partitioning** — Vector namespace names are scoped by the authenticated tenant ID
   (e.g., tenant-a's `embeddings` namespace is stored separately from tenant-b's `embeddings`),
   preventing namespace-name collision or cross-tenant access
3. **Crypto Isolation** (optional) — A tenant with dedicated isolation (`barrier`, `volume` or `raft`) has its own barrier, whose DEK encrypts that tenant's KV data.
   Vector data is encrypted at rest under the node's seal barrier; its tenant isolation comes from the namespace partitioning in step 2, not a per-tenant vector DEK.
   A dedicated-isolation tenant also chooses how that barrier's key is managed — self-managed (default) or system-managed auto-unseal — see
   [seal/multi-tenant.md → Tenant barrier key management](seal/multi-tenant.md#tenant-barrier-key-management)

Tenant and namespace names are validated to exclude control bytes (including the
internal `\x1f` namespace-scoping separator), preventing key-injection attacks.

## Tenant Isolation Modes

> Two different axes share the word "isolation". This section covers the
> **cluster-wide isolation-policy floor** (`strict` / `operator-access`;
> see [multi-tenancy/architecture.md#the-isolation-policy](multi-tenancy/architecture.md#the-isolation-policy)),
> set by the config key `tenant.isolation_policy`. The separate
> **per-tenant isolation ladder** (`prefix` / `barrier` / `volume` / `raft`), which controls how strongly an individual
> tenant's data is separated, is documented in
> [multi-tenancy/architecture.md](multi-tenancy/architecture.md).

Under either floor an un-tenanted identity (an mTLS certificate with no
`TenantID`) resolves to the `system` tenant. The floor decides one
thing: whether the vector service creates a namespace for a caller acting as
the `system` tenant.

### `operator-access` (the default; an unset floor reads as this)

- Vector namespace creation is **not refused** for system-tenant identities. All
  un-owned namespaces pool into `system`.
- **This is NOT cross-tenant isolation.** It is a single-tenant convenience
  for local development, single-operator deployments, or trusted internal tools where
  namespace ownership is not a security boundary.

### `strict`

- Vector namespace creation is **refused** (`403 Forbidden`, `tenant required`) when
  the authenticated identity resolves to the `system` tenant, preventing accidental
  un-owned namespace creation.
- Required for multi-tenant SaaS, HIPAA, PCI-DSS, or any deployment where namespace
  ownership must be enforced per authenticated identity.
- Tighten-only: cannot be lowered back to `operator-access` once set. A node in
  FIPS 140 mode (`crypto/fips140.Enabled()`: built with `GOFIPS140=latest`, or
  run with `GODEBUG=fips140=on`) tightens the floor to `strict` at start.

See [multi-tenancy/architecture.md#the-isolation-policy](multi-tenancy/architecture.md#the-isolation-policy)
for the full setting, including the per-tenant policy.

## Error Cases

| Condition | HTTP Status | Error Message |
|-----------|-------------|---------------|
| Database does not exist | 404 | `database not found` |
| Isolation type change on a tenant that holds data | 409 | `tenant: has data, isolation type cannot be changed` |
| Isolation type change when the tenant's data cannot be checked | 503 | `tenant: cannot verify the tenant holds no data, isolation type cannot be changed` |
| Strict floor, caller acting as the `system` tenant | 403 | `tenant required` (vector namespace creation) |

## Multi-Database Patterns

A tenant (authenticated identity) can use multiple logical databases for different
workloads, selected via the database scope:

```
# Same tenant (one identity), different databases:
X-QRDB-Database: users        # user service
X-QRDB-Database: analytics    # analytics service
X-QRDB-Database: prod         # production data
```

Each database has its own secondary indexes and scheduled-compaction policy.
Multi-tenant SaaS isolation is achieved by issuing each customer an identity in
their own tenant — the tenant boundary is then enforced by authentication,
not by a request parameter.

## See Also

- [multi-tenancy/architecture.md](multi-tenancy/architecture.md) — Isolation ladder, quotas, and entitlements
- [databases/README.md](databases/README.md) — Database management and lifecycle
- [seal/multi-tenant.md](seal/multi-tenant.md) — Tenant encryption isolation
- [api-server/api.md](api-server/api.md) — REST and gRPC API reference
