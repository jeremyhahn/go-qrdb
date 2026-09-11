# Multi-Tenancy — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-10

Two gRPC services: tenant lifecycle and governance on
`qrdb.v1.QRDBService`, and the per-tenant barrier on the dedicated
`qrdb.v1.TenantBarrierService`. The Unix socket transport serves the same gRPC
wire over a UDS.

## Capability matrix — tenant lifecycle and governance

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded (`Client.Tenants()`) | `scp` permission |
|---|---|---|---|---|---|---|
| Create | `POST /v1/tenants` | `TenantCreate` | `tenant.Create` | `qrdb_tenant_create` | `CreateTenant` | `tenants:create` |
| Get | `GET /v1/tenants/{id}` | `TenantGet` | `tenant.Get` | `qrdb_tenant_get` | `GetTenant` | `tenants:read` |
| List | `GET /v1/tenants` | `TenantList` | `tenant.List` | `qrdb_tenant_list` | `ListTenants` | `tenants:list` |
| Update | `PATCH /v1/tenants/{id}` | `TenantUpdate` | `tenant.Update` | `qrdb_tenant_update` | `UpdateTenant` | `tenants:update` |
| Delete | `DELETE /v1/tenants/{id}` | `TenantDelete` | `tenant.Delete` | `qrdb_tenant_delete` | `DeleteTenant` | `tenants:delete` |
| Update quotas | `PATCH /v1/tenants/{id}/quotas` | `TenantUpdateQuotas` | `tenant.UpdateQuotas` | `qrdb_tenant_update_quotas` | `UpdateTenantQuotas` | `tenants:update_quotas` |
| Update entitlements | `PATCH /v1/tenants/{id}/entitlements` | `TenantUpdateEntitlements` | `tenant.UpdateEntitlements` | `qrdb_tenant_update_entitlements` | `UpdateTenantEntitlements` | `tenants:set_entitlements` |

Provisioning-governance permissions checked in addition where relevant:
`tenants:set_isolation`, `databases:create_raft`, `databases:create_volume`.

## Capability matrix — per-tenant barrier

Served by `qrdb.v1.TenantBarrierService`.

| Capability | REST | gRPC / Unix (`TenantBarrierService/`) | QUIC | MCP tool | `scp` permission |
|---|---|---|---|---|---|
| Init | `POST /v1/tenants/{id}/barrier/init` | `Init` | `tenantbarrier.Init` | `qrdb_tenant_barrier_init` | **identity-gated** |
| Status | `GET /v1/tenants/{id}/barrier/status` | `Status` | `tenantbarrier.Status` | `qrdb_tenant_barrier_status` | **identity-gated** |
| Unseal | `POST /v1/tenants/{id}/barrier/unseal` | `Unseal` | `tenantbarrier.Unseal` | `qrdb_tenant_barrier_unseal` | **identity-gated** |
| Unseal share | `POST /v1/tenants/{id}/barrier/unseal-share` | `UnsealShare` | `tenantbarrier.UnsealShare` | `qrdb_tenant_barrier_unseal_share` | **identity-gated** |
| Seal | `POST /v1/tenants/{id}/barrier/seal` | `Seal` | `tenantbarrier.Seal` | `qrdb_tenant_barrier_seal` | `TenantBarrierSeal` |
| Shamir seal vote | `POST /v1/tenants/{id}/barrier/seal-vote` | `ShamirSealVote` | `tenantbarrier.ShamirSealVote` | `qrdb_tenant_barrier_shamir_seal_vote` | `TenantBarrierSealVote` |
| Officer register | `POST /v1/tenants/{id}/barrier/officers` | `OfficerRegister` | `tenantbarrier.OfficerRegister` | `qrdb_tenant_barrier_officer_register` | `TenantBarrierOfficerRegister` |
| Officer list | `GET /v1/tenants/{id}/barrier/officers` | `OfficerList` | `tenantbarrier.OfficerList` | `qrdb_tenant_barrier_officer_list` | `TenantBarrierOfficerList` |
| Officer claim status | `GET /v1/tenants/{id}/barrier/officers/claim-status` | `OfficerClaimStatus` | `tenantbarrier.OfficerClaimStatus` | `qrdb_tenant_barrier_officer_claim_status` | `TenantBarrierOfficerList` |
| Share claim begin | `POST /v1/tenants/{id}/barrier/officers/{username}/claim/begin` | `ShareClaimBegin` | `tenantbarrier.ShareClaimBegin` | `qrdb_tenant_barrier_share_claim_begin` | **identity-gated** |
| Share claim complete | `POST /v1/tenants/{id}/barrier/officers/{username}/claim/complete` | `ShareClaimComplete` | `tenantbarrier.ShareClaimComplete` | `qrdb_tenant_barrier_share_claim_complete` | **identity-gated** |

**Identity-gated** means the caller's own authenticated identity authorizes the
call — a tenant acts on its own barrier. Seal and officer registration carry
named permissions because they are administrative acts on the tenant.

These endpoints apply only to tenants with a **dedicated** isolation type. A
tenant on the shared system barrier has no barrier of its own.

## Scoping — how the tenant is determined

**The tenant comes from the authenticated mTLS identity (certificate OU), never
from a request field, header or metadata value.** The database is selected
separately by `X-QRDB-Database` or its per-transport equivalent. Detail:
[scoping.md](../scoping.md).

## Schemas

`POST /v1/tenants`:

```json
{ "id": "acme", "name": "Acme Corp",
  "isolation_type": "volume",
  "data_root": "/mnt/tenant-disks/acme",
  "barrier_auto_unseal": false }
```

`isolation_type`: `prefix` (default) · `barrier` · `volume` · `raft`.
`data_root` pins the dedicated `DataRoot` to an absolute path instead of
allocating from the volume pool, and must name an empty or not-yet-existing
directory. `barrier_auto_unseal` selects **system-managed** auto-unseal over the
secure self-managed default and is **entitlement-gated**.

Quotas — `-1` unlimited, `0` system default:

```json
{ "max_databases": 20, "max_raft_groups": 5,
  "max_replicas_per_group": 3, "max_total_replicas": 9,
  "max_storage_bytes": 107374182400, "max_database_size_bytes": 10737418240,
  "system_barrier_quota": 0 }
```

Entitlements:

```json
{ "allowed_isolation_modes": ["prefix","volume","raft"],
  "allow_dedicated_raft": true, "allow_volume_storage": true,
  "allow_system_managed_barrier": false,
  "allowed_barrier_modes": ["passphrase","shamir"] }
```

`allowed_barrier_modes` empty means built-in software defaults with **hardware
fail-closed**.

Tenant barrier init takes `strategy` (`none`/`passphrase`/`shamir`/a registered
hardware id; empty means `passphrase`), `passphrase`, and `threshold`/`shares`
for shamir. **Share material is never returned** — officers claim individually.

`passphrase` is a **byte string** — base64 in REST JSON, `bytes` in protobuf,
`[]byte` in the Go SDK — matching the `key` field the unseal endpoint already
uses. A Go string cannot be zeroized, so a credential is never one: the caller
may wipe the slice it supplied as soon as the call returns, and the daemon
wipes the slice its decoder produced before the handler returns. See
[../barrier-seal/api.md](../barrier-seal/api.md#the-barrier-credential-is-bytes-and-therefore-base64-in-rest-json).

## Delete semantics

`DELETE /v1/tenants/{id}` (and its peers on every transport) is **permanent in
both directions**: it reclaims the tenant's data AND retires the id.

- It **refuses a reserved id** — `system`, `default`, anything starting with
  `_`, anything containing `:` — with 400, before it reads anything. The
  platform's own tenant is not deletable through the API, and no flag makes it
  so.
- It **refuses while the tenant still owns databases**, naming them. Drop them
  first.
- It **destroys the tenant's data encryption key** before anything else.
- It **records the id as deleted.** A later `POST /v1/tenants` with the same id
  is refused with 409, permanently. There is no release, no retention window
  and no force flag.
- It **reclaims the tenant's keyspace** synchronously before returning.

Re-issuing a delete that failed part-way is the supported remedy and is safe:
the reclamation is idempotent, and a tenant whose metadata is already gone but
whose id is retired resumes rather than reporting 404. An id that was never
used is still 404.

## Governance ordering

**Entitlements are checked before quotas.** A plan that does not include a tier
is refused without consulting usage counters at all, so counter state never
leaks to a caller with no access to the tier.

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success | 200 | `OK` |
| Isolation change with data present | 409 | `FAILED_PRECONDITION` — `ErrTenantHasData` |
| Mode or feature not entitled | 403 | `PERMISSION_DENIED` — `*EntitlementError` |
| Seal strategy not in `allowed_barrier_modes` | 403 | `PERMISSION_DENIED` — hardware fail-closed by default |
| Quota exceeded | 429 | `RESOURCE_EXHAUSTED` |
| Dedicated tier without `storage.volume_pools` | 503 | `FAILED_PRECONDITION` — `volume pool not configured on barrier registry` |
| Barrier endpoint on a `prefix` tenant | 409 | `FAILED_PRECONDITION` |
| Tenant barrier sealed | 503 | `UNAVAILABLE` |
| Unknown tenant, tenant service configured | 404 | `NOT_FOUND` |
| Create **or delete** a RESERVED tenant id | 400 | `INVALID_ARGUMENT` — `*ReservedTenantIDError`. Previously unmapped on every transport and reported as 500 / `INTERNAL` |
| Create under a DELETED tenant id | 409 | `ALREADY_EXISTS` — `*TenantDeletedError`, carries the retirement time |
| Delete while the tenant owns databases | 409 | `FAILED_PRECONDITION` — `*TenantHasDatabasesError`, names them |
| Delete: DEK shred failed | 500 | `INTERNAL` — `*DEKShredError`; nothing changed |
| Delete: keyspace reclaim failed | 500 | `INTERNAL` — `*KeyspaceReclaimError`; the id is already retired, retry to resume |
| Missing permission | 403 | `PERMISSION_DENIED` |

> **Deployment gap, recorded deliberately.** If the data-plane service registry
> is wired **without** a tenant service — a backward-compatibility path for
> single-tenant embeddings — the unknown-tenant rejection is **skipped**. Any
> tenant name presented by an authenticated client is accepted; it is still
> namespaced and barrier-checked, but **not validated against a tenant
> registry**. Production multi-tenant deployments must configure the tenant
> service.

## Pagination

`ListTenants` returns every tenant and is not paginated.

## Versioning

Tenant REST paths are under `/v1/`. Nothing here is deprecated.
