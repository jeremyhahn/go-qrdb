# Multi-Tenancy — API

> **Status: NORMATIVE** (the strict-isolation section is RULED, not yet
> implemented) · Scope: go-qrdb · Last verified: 2026-10-06
> Conflicts with this document are defects. Fix them here first.

Two gRPC services: tenant lifecycle and governance on
`qrdb.v1.QRDBService`, and the per-tenant barrier on the dedicated
`qrdb.v1.TenantBarrierService`. The Unix socket transport serves the same gRPC
wire over a UDS.

## Capability matrix — tenant lifecycle and governance

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded (`Client.Tenants()`) | `scp` permission |
|---|---|---|---|---|---|---|
| Create | `POST /v1/tenants` | `TenantCreate` | `tenant.Create` | `qrdb_tenant_create` | `CreateTenant` | `tenants:create` (platform operator) |
| Get | `GET /v1/tenants/{id}` | `TenantGet` | `tenant.Get` | `qrdb_tenant_get` | `GetTenant` | `tenants:read` |
| List | `GET /v1/tenants/list` | `TenantList` | `tenant.List` | `qrdb_tenant_list` | `ListTenants` | `tenants:list`; the full list needs `tenants:list:cross` |
| Update | `PUT /v1/tenants/{id}` | `TenantUpdate` | `tenant.Update` | `qrdb_tenant_update` | `UpdateTenant` | `tenants:update`; an isolation change also needs `tenants:set_isolation`, a status change `tenants:set_status` (platform operator) |
| Delete | `DELETE /v1/tenants/{id}` | `TenantDelete` | `tenant.Delete` | `qrdb_tenant_delete` | `DeleteTenant` | `tenants:delete` |
| Update quotas | `POST /v1/tenants/{id}/quotas` | `TenantUpdateQuotas` | `tenant.UpdateQuotas` | `qrdb_tenant_update_quotas` | `UpdateTenantQuotas` | `tenants:set_quotas` (platform operator) |
| Update entitlements | `PATCH /v1/tenants/{id}/entitlements` | `TenantUpdateEntitlements` | `tenant.UpdateEntitlements` | `qrdb_tenant_update_entitlements` | `UpdateTenantEntitlements` | `tenants:set_entitlements` (platform operator) |
| List deleted (retired ids) | `GET /v1/tenants/deleted/list` | `TenantListDeleted` | `tenant.ListDeleted` | `qrdb_tenant_list_deleted` | `ListDeletedTenants` | `tenants:list_deleted` |
| Reconcile deleted (this node) | `POST /v1/tenants/deleted/reconcile` | `TenantReconcileDeleted` | `tenant.ReconcileDeleted` | `qrdb_tenant_reconcile_deleted` | `ReconcileDeletedTenants` | `tenants:reconcile_deleted` |

The last two rows carry **cross-scope** permissions: they are effective only
for an identity in the system tenant, because a retirement names ids across
every tenant and the reconcile acts on a node's disk across every tenant.

Provisioning-governance permissions checked in addition where relevant:
`tenants:set_isolation`, `databases:create_raft`, `databases:create_volume`.

> **Corrected 2026-09-29.** This table previously gave the REST routes as
> `GET /v1/tenants`, `PATCH /v1/tenants/{id}` and `PATCH /v1/tenants/{id}/quotas`,
> and the quotas permission as `tenants:update_quotas`. None of those exist. The
> routes are `GET /v1/tenants/list`, `PUT /v1/tenants/{id}` and
> `POST /v1/tenants/{id}/quotas`, and quotas are gated by `tenants:set_quotas`
> (before this release, by `tenants:update`).

## Who may call what

The tenant a request NAMES is not the tenant it runs in, so every network
transport (REST, gRPC, the Unix socket, QUIC, MCP) makes a second decision
after its per-route permission check. The built-in Admin role holds
`tenants:read`, `tenants:update`, `tenants:delete` and `tenants:list` in every
tenant, and these rules are what keep a tenant's Admin inside its own tenant.

- **A tenant's plan is set by the platform operator only.** Creating a tenant
  and setting its quotas, its entitlements or its isolation tier need
  `tenants:create`, `tenants:set_quotas`, `tenants:set_entitlements` and
  `tenants:set_isolation`. All four are **cross-scope**: only an identity in the
  `system` tenant can exercise them, whichever tenant the request names. The
  built-in Admin role carries them in every tenant, as it carries its other
  cross-scope grants, so only Admin in the `system` tenant can use them. A
  tenant's Admin cannot raise its own quotas, grant itself entitlements, change
  its own isolation tier, or create tenants. The embedded transport runs no
  AuthMiddleware, so it enforces this rule itself
  (`tenant.RequirePlatformOperator`): `CreateTenant`, `UpdateTenantQuotas`,
  `UpdateTenantEntitlements`, and an isolation-tier change through
  `UpdateTenant`, are refused with `*tenant.PlatformOperatorRequiredError`
  for a caller whose context identity is not in the `system` tenant. A
  context with no identity bound at all (a trusted in-process host with no
  auth wired up) resolves to the `system` tenant, as it already does for
  `tenant.Confine`.
- **Get, update and delete are authorized against the tenant they name.** A
  tenant's Admin may read, rename and delete its own tenant and no other; the
  platform operator may act on any tenant. Deleting its own tenant is
  deliberately within a tenant Admin's reach:
  `tenants:delete` stays self-scoped.
- **Update carries only what a tenant may edit on itself.** `tenants:update`
  covers the display name. A request that carries `isolation_type` needs
  `tenants:set_isolation` as well, and is refused whole without it. The check
  is on the field's presence, not on whether it changes anything: a request
  naming the tenant's CURRENT isolation type still needs
  `tenants:set_isolation`, and is then a no-op for the isolation type -- it
  moves no data and consults no data probe (see below).
- **Changing the isolation type requires an empty tenant, on every
  transport.** The tenant service refuses the change with 409 /
  `FAILED_PRECONDITION` (`ErrTenantHasData`) while the tenant holds any data,
  relational tables included. When the node cannot verify that the tenant is
  empty -- the data plane is unreachable, or no data probe is wired -- the
  change is refused with 503 / `UNAVAILABLE` rather than risk orphaning data.
  Only REST enforced this before.
- **List is narrowed, not refused.** A caller holding `tenants:list:cross`
  (the platform operator) gets every tenant. Anyone else gets only its own
  tenant, because the full list carries every tenant's configuration,
  `data_root` included.
- **A tenant id that cannot name a tenant is refused before anything else.**
  An id containing `/`, `\` or a control byte, or the relative segments `.` and
  `..`, is a 400 / `INVALID_ARGUMENT`, on create and on every by-id operation.
  On REST a `/` in the URL is a path separator, so a by-id route never sees
  one; it reaches a tenant id only through the create body.
- **The barrier bootstrap windows do not override the rate limiter.** The
  trust-on-first-init and sealed-window fallbacks admit a tenant's own identity
  when RBAC cannot, but a refusal from a rate-limit policy (for example on
  `tenant_barrier:unseal`) is final: 429 / `RESOURCE_EXHAUSTED`.

The embedded transport (`Client.Tenants()`) runs in the caller's process and
runs no AuthMiddleware, so most of this section's per-route checks do not
apply to it; the platform-operator rule above is the exception it enforces
directly.

## Capability matrix — per-tenant barrier

Served by `qrdb.v1.TenantBarrierService`.

| Capability | REST | gRPC / Unix (`TenantBarrierService/`) | QUIC | MCP tool | `scp` permission |
|---|---|---|---|---|---|
| Init | `POST /v1/tenants/{id}/barrier/init` | `Init` | `tenant.BarrierInit` | `qrdb_tenant_barrier_init` | **identity-gated** |
| Status | `GET /v1/tenants/{id}/barrier/status` | `Status` | `tenant.BarrierStatus` | `qrdb_tenant_barrier_status` | **identity-gated** |
| Unseal | `POST /v1/tenants/{id}/barrier/unseal` | `Unseal` | `tenant.BarrierUnseal` | `qrdb_tenant_barrier_unseal` | **identity-gated** |
| Seal | `POST /v1/tenants/{id}/barrier/seal` | `Seal` | `tenant.BarrierSeal` | `qrdb_tenant_barrier_seal` | `TenantBarrierSeal` |
| Officer register | `POST /v1/tenants/{id}/barrier/officers` | `OfficerRegister` | `tenant.BarrierOfficerRegister` | `qrdb_tenant_barrier_officer_register` | `TenantBarrierOfficerRegister` |
| Officer list | `GET /v1/tenants/{id}/barrier/officers` | `OfficerList` | `tenant.BarrierOfficerList` | `qrdb_tenant_barrier_officer_list` | `TenantBarrierOfficerList` |
| Share claim begin | `POST /v1/tenants/{id}/barrier/officers/{username}/claim/begin` | `ShareClaimBegin` | `tenant.BarrierShareClaimBegin` | `qrdb_tenant_barrier_share_claim_begin` | **identity-gated** |
| Share claim complete | `POST /v1/tenants/{id}/barrier/officers/{username}/claim/complete` | `ShareClaimComplete` | `tenant.BarrierShareClaimComplete` | `qrdb_tenant_barrier_share_claim_complete` | **identity-gated** |

Share-holder quorum rounds, rekey and revocation are listed in
[../shamir-custody/api.md](../shamir-custody/api.md#capability-matrix--tenant-barrier-share-holders).
A Shamir tenant barrier is unsealed and sealed only through its holder quorum.

### Init's inline Shamir officer roster

`TenantBarrierInitRequest` carries an optional `officers` field (the same
`username`/`role`/`csr_pem`/`encryption_kem`/`encryption_key`/`factors`/
`designated` shape as `OfficerRegister`) and a `policy` field (factor
policy, `""` = `designated`). Supplying `officers` on the Shamir strategy
validates the whole roster as one pure batch — before any of them is
registered — then registers every entry atomically with initialization
through the same holder engine `OfficerRegister` uses, rolling back every
officer already registered on any later failure (a registration conflict,
or the barrier's own key split). The response's `holders` field then
carries each officer's issued certificate, in roster order. Supplying
`officers` on any strategy but Shamir is refused (not silently discarded);
an empty/absent `officers` is the pre-enrolled-holders path (every holder
already registered standalone via `OfficerRegister`), unchanged from before
this field existed.

`officers`/`holders` are wired on every transport: REST, gRPC, QUIC, MCP,
Unix and the embedded transport. gRPC and Unix carry `officers` as
`repeated CeremonyOfficer` (the same message `qrdb init`'s own inline
roster uses for the system barrier) and `holders` as `repeated
CeremonyHolderCert`.

**Identity-gated** means the caller's own authenticated identity authorizes the
call — a tenant acts on its own barrier, and a share holder authorizes itself
with its certificate or factor proof. Seal and officer registration carry
named permissions because they are administrative acts on the tenant.

These endpoints apply only to tenants with a **dedicated** isolation type. A
tenant on the shared system barrier has no barrier of its own.

## Scoping — how the tenant is determined

**The tenant comes from the authenticated mTLS identity (certificate OU), never
from a request field, header or metadata value.** The database is selected
separately by `X-QRDB-Database` or its per-transport equivalent. Detail:
[scoping.md](../scoping.md).

## Schemas

`POST /v1/tenants` creates a tenant **with its first admin**, or not at all:

```json
{ "id": "acme", "name": "Acme Corp",
  "isolation_type": "volume",
  "barrier_auto_unseal": false,
  "admin_username": "alice",
  "admin_csr_pem": "<PEM CERTIFICATE REQUEST>" }
```

`isolation_type`: `prefix` (default) · `barrier` · `volume` · `raft`.
`barrier_auto_unseal` selects **system-managed** auto-unseal over the secure
self-managed default and is **entitlement-gated**. A request carrying
`data_root` is refused (`*DataRootRetiredError`, 400 / `INVALID_ARGUMENT`): a
tenant's location is a volume pool and slot, resolved on each node. In a
response, `data_root` is the tenant's directory **on the node that answered**,
resolved from that pool and slot; it is never stored.

The first admin:

| Field | Meaning |
|---|---|
| `admin_username` | required; the admin's account name |
| `admin_csr_pem` | the admin's certificate signing request: a `prefix` tenant's own CA signs it; for a `barrier`, `volume` or `raft` tenant the cluster CA signs it. A node with no cluster CA (bring-your-own TLS) signs a system-managed tenant's with the tenant's CA and refuses a self-managed one's; send `admin_cert_pem` instead |
| `admin_cert_pem` | the admin's own certificate, leaf first; must chain to `trust_anchors_pem` |
| `admin_chain_pem` | optional intermediates for `admin_cert_pem` |
| `trust_anchors_pem` | CA certificates registered as the tenant's trust anchors in the same call; required with `admin_cert_pem` |

Exactly one of `admin_csr_pem` and `admin_cert_pem`. Only the CSR's key is
used. A bring-your-own certificate must chain to one of `trust_anchors_pem`
for client authentication, its CN must be `admin_username`, and it must name
the new tenant.

Response `201` — the tenant's fields, plus the admin. **There is never a
private key:**

```json
{ "id": "acme", "name": "Acme Corp", "isolation_type": "volume", "...": "...",
  "admin": { "user_id": 42, "username": "alice", "cert_pem": "<PEM>",
             "serial": "3f2a9c…", "thumbprint": "<SPKI SHA-256>" } }
```

`serial` is omitted for a bring-your-own certificate. On gRPC, QUIC and MCP the
same response is `{ "tenant": {…}, "admin": {…} }` (`TenantCreateResponse`),
with the same admin fields.

For a `barrier`, `volume` or `raft` tenant the admin's account cannot be written
yet — it lives behind the tenant's own barrier — so `user_id` is omitted and
`"awaits_barrier_init": true` is set. The certificate is recorded as the
tenant's pending admin; the barrier's trust-on-first-init window then admits
**only** that certificate, presented as that username, and the account is
created, granted the tenant's Admin role and bound to it as soon as the barrier
is usable (on init, on every unseal, and on every status read that finds it
unsealed, until it succeeds).

A system-managed (`barrier_auto_unseal`) tenant's barrier is usable at
creation, so its response omits both `user_id` and `awaits_barrier_init`: the
account is completed right **after** the response, not inside the request.
That step writes through the tenant's own shard and can take longer than the
request's deadline; it runs on its own bound (each attempt cut off at 10s,
retried with backoff for up to 2 minutes), and a step that still fails leaves
the pending record for the next unseal or status read to finish. Until the
account exists, a request made with the admin's certificate is refused — 401
(`CertBindingRequiredError`) under the default `mtls_binding_policy: required`,
403 otherwise — so a client that calls straight after the create retries that
refusal for a few seconds.

**The response is the record the create committed.** The create does not read
the tenant back after committing it, so a slow read cannot turn a committed
create into an error.

**The answering node already trusts the returned certificate.** A CSR-issued
admin certificate is signed by the tenant's new login CA. Before answering, the
create rebuilds the answering node's client-certificate trust, so the next TLS
handshake on that node admits the certificate. Other nodes pick up the new CA
within their 2-second rebuild interval. If the rebuild fails, the failure is
logged and the create still answers; the next periodic rebuild admits the
certificate.

**One unit.** Every input that can be refused — the anchors, the CSR, the
certificate's chain and identity — is judged before anything is written, so a
refusal leaves no tenant and the id stays usable. A failure after the first
write undoes every write in reverse order. Undoing deletes the tenant record,
and a deleted id is retired (below), so that id cannot be created again; any
undo step that itself failed is listed in the error (`ProvisioningUndoneError`)
for an operator to finish.

**Deadline.** Running out of time is such a failure, so the deadline is sized
for the whole sequence of Raft writes -- the tenant record, its pool
reservation and barrier, then roles, the admin's account and its certificate
binding -- not for one read. The server bounds onboarding itself, the same on
every transport: 60 seconds (`service.DefaultTenantOnboardingTimeout`), or the
caller's own deadline when that is sooner. REST's other tenant endpoints use
5 seconds. A caller that cancels or disconnects still cancels the creation,
and the undo then runs on its own 30-second budget. An onboarding the deadline
cuts short answers 504 / `DEADLINE_EXCEEDED`; because undoing retires the id,
retry under a new id.

**BREAKING:** `admin_username` and one admin credential are required, and the
response carries `admin`. A tenant can no longer be created without its first
admin. The caller still needs `tenants:create` (platform operator).

### Description and metadata

A tenant carries two free-form descriptive fields, returned on every read
(`TenantInfo` / `TenantResponse`) and set only by **Update**, a patch. Neither
field affects isolation, quotas or authorization; qrdb stores them and returns
them.

| Field | Patch semantics | Limits |
|---|---|---|
| `description` | absent: unchanged · present: replaces · `""`: clears | at most 1024 bytes of UTF-8 |
| `metadata` | absent: unchanged · present: **replaces the whole map** · empty map: clears | at most 64 entries; keys non-empty, at most 128 bytes; values at most 1024 bytes; all UTF-8 |

On each transport:

| Transport | `description` | `metadata` |
|---|---|---|
| REST `PUT /v1/tenants/{id}` | `"description"`; omitted or `null` is unchanged | `"metadata": {…}`; omitted or `null` is unchanged, `{}` clears |
| gRPC / Unix `TenantUpdate`, QUIC `tenant.Update` | `optional string description` | `TenantMetadata metadata` (`map<string,string> entries`); an unset message is unchanged, a set empty message clears |
| MCP `qrdb_tenant_update` | `description` string | `metadata` as a JSON object string; `clear_metadata: true` clears when `metadata` is absent |
| Go SDK `UpdateTenantRequest`, every SDK transport (embedded included) | `Description *string`: nil unchanged, `""` clears | `Metadata map[string]string`: nil unchanged, non-nil empty clears |

```json
PUT /v1/tenants/acme
{ "description": "Acme production",
  "metadata": { "env": "prod", "team": "storage" } }
```

A value over a limit is refused before anything is written —
`*tenant.TenantFieldError` naming the field, 400 / `INVALID_ARGUMENT` — so a
refused update changes none of the tenant's fields.

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

### Status

Every tenant read returns `status`: `active`, `suspended` or `disabled`. A
tenant is created `active`. The update request's optional `status` moves it;
absent leaves it unchanged. Any status may move to any other except
`disabled` → `suspended`; a disabled tenant is reactivated first.

| Transport | Field |
|---|---|
| REST `PUT /v1/tenants/{id}` | `"status"`; omitted or `null` is unchanged |
| gRPC / Unix `TenantUpdate`, QUIC `tenant.Update` | `optional string status`; `TenantResponse.status` |
| MCP `qrdb_tenant_update` | `status` string |
| Go SDK `transport.UpdateTenantRequest` | `Status *TenantStatus`: nil unchanged; `TenantInfo.Status` |
| CLI | `qrdb tenant update <id> --status suspended`; `tenant get` / `list` show it |

A status change needs `tenants:set_status`, which is cross-scope: only a
system-tenant Admin holds it, so a tenant's own Admin cannot suspend or
reactivate its tenant. While a tenant is not active the auth chain refuses
every principal of it on every transport and credential — a token issued
before the change included — with 403 / `PERMISSION_DENIED`. Reactivating it
restores access on the next request. Principals of other tenants and the
system administrator's calls on the tenant are unaffected.

Every status change is audited as `tenant.status_change`, keyed by the
affected tenant and carrying the caller and the `from` and `to` status. A
`permitted` record is written before the change is stored and a `success` or
`failure` record once it is. Under the default `fail-closed`
`auth.audit_failure_policy` a change whose record cannot be written is
refused and nothing is stored. An update that leaves the status as it was
records nothing.

```json
PUT /v1/tenants/acme
{ "status": "suspended" }
```

## Delete semantics

`DELETE /v1/tenants/{id}` (and its peers on every transport) is **permanent in
both directions**: it reclaims the tenant's data AND retires the id.

- It **refuses a reserved id** — `system`, `default`, `custodian`, `security-officer`, anything starting with
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

## Retired tenant ids

A deleted id is retired permanently, and the retirements are enumerable.

`GET /v1/tenants/deleted/list?page_size=2&page_token=...` returns one page in
ascending id order:

```json
{ "tombstones": [
    { "id": "acme", "name": "Acme Corp", "isolation_type": "volume",
      "data_root": "<install-dir>/pool/acme",
      "created_at": "2026-08-01T10:00:00Z",
      "deleted_at": "2026-09-20T14:31:07Z" } ],
  "next_page_token": "YWNtZQ" }
```

- `page_size` `0` or absent uses the default (100); larger than the maximum
  (1000) is served at the maximum; negative is refused with 400 /
  `INVALID_ARGUMENT`.
- `page_token` is opaque: pass back the previous page's `next_page_token`. A
  value that is not a valid token is refused with 400 / `INVALID_ARGUMENT`.
  `next_page_token` is absent on the last page.
- A record whose body cannot be decoded is still listed, by id, with a zero
  `deleted_at`: the id is retired either way.
- `data_root` is the path on the node that ran the delete.

`POST /v1/tenants/deleted/reconcile` reclaims, **on the node that serves the
request**, the directories of retired tenant ids — what a replica keeps when it
was down, or not hosting the tenant, when the tenant was deleted. It is never
forwarded to the leader: run it against each node.

```json
{ "inspected": 3,
  "reclaimed": [ { "tenant_id": "acme", "path": "<install-dir>/pool/acme" } ],
  "skipped":   [ { "tenant_id": "old", "path": "<install-dir>/disks/shared",
                   "reason": "overlaps-live-tenant" } ],
  "failed":    [ { "tenant_id": "gone", "op": "remove",
                   "path": "<install-dir>/disks/gone", "error": "..." } ] }
```

**A pass that failed for some ids still answers with its report.** `failed`
lists each retired id the pass could not fully reclaim — the step (`op`), the
path when the step is about one, and the node's message — and the others are
still reclaimed. The status is success (200 / `OK`) because the response is
the report; the failures are in it. A failed id keeps its records, so running
the reconcile again retries it. Only a failure to read the pass's guards (the
tombstones, or the live tenant set) is an error status, with no report.

The Go SDK returns **both** for a partly failed pass: the response, with
`Failed` populated, and a `*transport.ReconcileDeletedTenantsError` naming the
same failures. On the embedded transport that error also unwraps to the
node's own `*tenant.ResidueReconcileFailuresError`.

The only predicate is the retirement record. A directory a live tenant claims
is never removed and is reported with `reason`:

| `reason` | Left in place because |
|---|---|
| `live-tenant-record` | the retired id also has a live tenant record; nothing of the id is touched (`path` empty) |
| `overlaps-live-tenant` | the path is, contains or lies inside a directory a live tenant claims |
| `overlaps-reserved-root` | the path is, or contains, a volume-pool base or a reserved system data root, or it lies inside a reserved root without lying strictly inside a volume-pool base |
| `not-canonical` | the path, as recorded, is not absolute and canonical (relative, or carrying `.`/`..` segments), so it is never resolved and removed |

Every skipped path is also logged on the node at `WARN`. A dedicated
directory that is a mount point is emptied and reported as reclaimed; the
mount itself is left for the operator to unmount.

The same reconcile runs at boot, before any tenant barrier is restored.

## Governance ordering

**Entitlements are checked before quotas.** A plan that does not include a tier
is refused without consulting usage counters at all, so counter state never
leaks to a caller with no access to the tier.

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success | 200 (201 for create) | `OK` |
| Isolation change with data present | 409 | `FAILED_PRECONDITION` — `ErrTenantHasData` |
| Mode or feature not entitled | 403 | `PERMISSION_DENIED` — `*EntitlementError` |
| Seal strategy not in `allowed_barrier_modes` | 403 | `PERMISSION_DENIED` — hardware fail-closed by default |
| Quota exceeded | 429 | `RESOURCE_EXHAUSTED` |
| Dedicated tier without `storage.volume_pools` | 503 | `FAILED_PRECONDITION` — `volume pool not configured on barrier registry` |
| Barrier endpoint on a `prefix` tenant | 409 | `FAILED_PRECONDITION` |
| Tenant barrier sealed | 503 | `UNAVAILABLE` |
| Unknown tenant, tenant service configured | 404 | `NOT_FOUND` |
| Tenant lookup that could not run: the system shard has no leader, or the lookup's 3 s bound expired | 503 / 504 | `UNAVAILABLE` / `DEADLINE_EXCEEDED` — classified as the cause, never `NOT_FOUND`: a lookup that did not run has not found the tenant absent |
| Create without `admin_username`, or with neither or both of `admin_csr_pem` / `admin_cert_pem` | 400 | `INVALID_ARGUMENT` — `*service.TenantOnboardingFieldError`, `*bootstrap.MissingAdminCredentialError`, `*bootstrap.ConflictingAdminCredentialError` |
| Create with an admin CSR or certificate that is refused, or invalid trust anchors | 400 | `INVALID_ARGUMENT` — classified as the cause (`cert.InvalidCSRError`, `trustanchor.CertificateNotTrustedError`, `certenroll.IdentityMismatchError`, `*bootstrap.InvalidTrustAnchorsError`); nothing written |
| Create with `admin_cert_pem` and no `trust_anchors_pem` | 409 | `FAILED_PRECONDITION` — `trustanchor.NoAnchorsError`; nothing written |
| Create a self-managed `barrier`, `volume` or `raft` tenant with `admin_csr_pem` on a node with no cluster CA | 409 | `FAILED_PRECONDITION` — `*ceremony.AdminCredentialRequiresCAError`; nothing written, so the id stays free |
| Create failed after a write and was undone | as the cause | `*bootstrap.ProvisioningUndoneError`; the id is retired; `UndoFailures` lists any undo step that failed |
| Tenant onboarding not configured on this node | 501 | `UNIMPLEMENTED` |
| Barrier init by any certificate other than the onboarded admin's | 403 | `PERMISSION_DENIED` — `*middleware.FirstInitCertificateMismatchError` |
| Barrier init, `auth.mtls_binding_policy: required`, tenant has no onboarded admin | 403 | `PERMISSION_DENIED` — `*middleware.FirstInitNotOnboardedError` |
| Create **or delete** a RESERVED tenant id | 400 | `INVALID_ARGUMENT` — `*ReservedTenantIDError`. Previously unmapped on every transport and reported as 500 / `INTERNAL` |
| Create under a DELETED tenant id | 409 | `ALREADY_EXISTS` — `*TenantDeletedError`, carries the retirement time |
| Delete while the tenant owns databases | 409 | `FAILED_PRECONDITION` — `*TenantHasDatabasesError`, names them |
| Delete: tombstone write or metadata delete failed | 500 | `INTERNAL` — `*StoreError`; **nothing changed**, the tenant and its key are intact |
| Delete: keyspace reclaim failed | 500 | `INTERNAL` — `*TeardownError` wrapping `*KeyspaceReclaimError`; the id is already retired, retry to resume |
| List deleted: bad `page_size` or `page_token` | 400 | `INVALID_ARGUMENT` — `*InvalidPageRequestError` |
| Reconcile deleted: a live tenant record is unreadable, or the tombstones cannot be listed | 500 | `INTERNAL` — `*ResidueReconcileError` naming the step; the pass stopped, and re-running resumes |
| Reconcile deleted: one or more retired ids failed | 200 | `OK` — the report, with `failed` naming each failed tenant, step, path and message; every other id was still reclaimed, a failed id keeps its records, and re-running resumes. The Go SDK returns the response and a `*transport.ReconcileDeletedTenantsError` |
| Create with `data_root` | 400 | `INVALID_ARGUMENT` — `*DataRootRetiredError`; nothing is created at the path |
| Read a tenant whose stored record names a path | 409 | `FAILED_PRECONDITION` — `*PersistedDataRootError`; the record is refused, never trusted, and its data is never opened. Listing still returns the tenant, marked refused, beside every other tenant. Delete it — the delete removes its records and touches no directory — and re-create it |
| Update with a `description` or `metadata` over its limit, an empty metadata key, or invalid UTF-8 | 400 | `INVALID_ARGUMENT` — `*TenantFieldError` naming the field; nothing written |
| Update with a `status` that is not `active`, `suspended` or `disabled` | 400 | `INVALID_ARGUMENT` — `*InvalidStatusError` listing the allowed values; nothing written |
| Update with a `status` the tenant cannot move to from its current one (`disabled` → `suspended`) | 409 | `FAILED_PRECONDITION` — `*StatusTransitionError`; nothing written |
| Any request by a principal of a suspended or disabled tenant | 403 | `PERMISSION_DENIED` — `*middleware.TenantNotActiveError`, classed as a disabled account |
| Missing permission | 403 | `PERMISSION_DENIED` |
| A tenant's Admin naming another tenant, or setting its own plan or status | 403 | `PERMISSION_DENIED` |
| Tenant id containing `/`, `\`, a control byte, or `.`/`..` | 400 | `INVALID_ARGUMENT` — `*InvalidTenantIDCharError` / `*InvalidTenantIDError`. Previously 500 / `INTERNAL` on create |
| Rate-limit policy exhausted, including inside a barrier bootstrap window | 429 | `RESOURCE_EXHAUSTED` |
| Isolation change while the tenant cannot be verified empty | 503 | `UNAVAILABLE` — `ErrTenantDataUnverifiable` / `*TenantDataProbeError` |
| A tenant read or write whose raft wait ran out (`quicraft: timeout`) | 503 | `UNAVAILABLE` — previously 500 / `INTERNAL`. On create it means the tenant's record did not commit within the deadline: the create answers with the record it committed and reads nothing back, and its admin-account step for a system-managed tenant runs after the answer. A proposal cut off by the deadline can still commit, so read the tenant before retrying; a create retried after its commit answers 409 |

> **Deployment gap, recorded deliberately.** If the data-plane service registry
> is wired **without** a tenant service — a backward-compatibility path for
> single-tenant embeddings — the unknown-tenant rejection is **skipped**. Any
> tenant name presented by an authenticated client is accepted; it is still
> namespaced and barrier-checked, but **not validated against a tenant
> registry**. Production multi-tenant deployments must configure the tenant
> service.

## Strict isolation — RULED

> **Status: ruled — partially implemented.** Design: [architecture.md](architecture.md#strict-tenant-isolation--design).
> Plan: [engineering.md](engineering.md#implementation-plan). Every row below
> ships on REST, gRPC, Unix, QUIC, MCP and embedded together, in one change, or
> not at all.
>
> **Correction (2026-10-05):** the first-admin-at-create capability below is
> already shipped, unconditionally — see the architecture correction linked
> above and the rows marked "already shipped".
>
> **Correction (2026-10-06):** the "Removed" list below is done, and done
> unconditionally rather than gated by the isolation-policy rows above it —
> see the architecture correction linked above for exactly what shipped at the
> Go service/RBAC layer. None of the table rows below are reachable from any
> transport yet: there is no REST/gRPC/QUIC/Unix/MCP/embedded surface for the
> isolation-policy get/set verbs, `tenant_identity:read`/`write`, or anything
> else in this table. `tenants:read:cross` and `tenants:delete:cross` exist as
> permissions and are consulted by `AuthorizeTenantTarget`'s existing
> `GET`/`DELETE /v1/tenants/{id}` routes (and their gRPC/QUIC/Unix/MCP/embedded
> equivalents) today — those two rows are the only ones with a live transport
> path, through routes that already existed before this ruling.

### New and changed capabilities

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded | `scp` permission |
|---|---|---|---|---|---|---|
| Read the cluster floor | `GET /v1/cluster/isolation-policy` | `IsolationPolicyGet` | `cluster.IsolationPolicyGet` | `qrdb_isolation_policy_get` | `IsolationPolicy` | any authenticated caller |
| Tighten the floor to `strict` | `PUT /v1/cluster/isolation-policy` | `IsolationPolicySet` | `cluster.IsolationPolicySet` | `qrdb_isolation_policy_set` | `SetIsolationPolicy` | `cluster:set_isolation_policy` (ScopeCross) |
| Opt a tenant into `strict` | `PUT /v1/tenants/{id}/isolation-policy` | `TenantSetIsolationPolicy` | `tenant.SetIsolationPolicy` | `qrdb_tenant_set_isolation_policy` | `SetTenantIsolationPolicy` | `tenants:set_isolation_policy` (ScopeSelf: the tenant's own Admin) |
| Create a tenant | `POST /v1/tenants` | `TenantCreate` | `tenant.Create` | `qrdb_tenant_create` | `CreateTenant` | `tenants:create`. `admin_username` plus `admin_csr_pem`/`admin_cert_pem` are **already required** (shipped 2026-10-03, independent of this ruling). What this ruling adds: the request may also carry `isolation_policy: strict`. |
| Read another tenant's registry entry | `GET /v1/tenants/{id}` | `TenantGet` | `tenant.Get` | `qrdb_tenant_get` | `GetTenant` | own tenant: `tenants:read`; any other: **`tenants:read:cross`** |
| Delete another tenant (S8) | `DELETE /v1/tenants/{id}` | `TenantDelete` | `tenant.Delete` | `qrdb_tenant_delete` | `DeleteTenant` | own tenant: `tenants:delete`; any other: **`tenants:delete:cross`**, audited |
| Tenant barrier init, as proof of possession of the first admin's key | `POST /v1/tenants/{id}/barrier/init/begin`, `…/init/complete` | `TenantBarrierService.InitBegin`, `InitComplete` | `tenant.BarrierInitBegin`, `tenant.BarrierInitComplete` | `qrdb_tenant_barrier_init_begin`, `qrdb_tenant_barrier_init_complete` | `Tenants().TenantBarrierInit` (in process: no connection to bind, no proof asked) | **identity-gated by the CSR key**: `complete` carries a signature by the pending first admin's key over the tenant, the `begin` nonce and the connection's TLS 1.3 exporter, and the owner's signed epoch-1 init statement (`init_statement`; see [../barrier-seal/api.md](../barrier-seal/api.md#status-and-error-mapping)), which the node claims before its key and from which every other serving node initialises its own. Today the client sends it to the node it is connected to; sending a passphrase or Shamir init to every serving node ships with phase 7 |
| Tenant barrier unseal and rekey | unchanged routes | unchanged | unchanged | unchanged | unchanged | unchanged; the client sends it to every serving node, and each node answers for itself |
| Operator: read a named tenant's users, roles, factors and bindings | `GET /v1/tenants/{id}/identity/users[/{uid}]`, `…/roles[/{rid}]` | `TenantIdentityRead` | `tenantidentity.Read` | `qrdb_tenant_identity_read` | `TenantIdentity().Read` | `tenant_identity:read` (ScopeCross; only where the target's effective policy is `operator-access`) |
| Operator: change a named tenant's identity records | `POST`, `PUT`, `DELETE` `/v1/tenants/{id}/identity/...` | `TenantIdentityWrite` | `tenantidentity.Write` | `qrdb_tenant_identity_write` | `TenantIdentity().Write` | `tenant_identity:write` (the same) |
| Back up a strict tenant's groups (S10) | `POST /v1/tenants/{id}/backups` | `TenantBackupCreate` | `tenant.BackupCreate` | `qrdb_tenant_backup_create` | `TenantBackups().Create` | `backup:tenant` (ScopeTenantOnly) |

> **Correction (2026-10-14):** the barrier-init row named the QUIC methods
> `tenantbarrier.InitBegin`/`InitComplete` and the embedded call
> `TenantBarrier().Init`. What shipped is `tenant.BarrierInitBegin`/
> `tenant.BarrierInitComplete`, beside the other `tenant.Barrier*` QUIC
> methods, and the existing `Tenants().TenantBarrierInit`, which on a network
> transport runs `begin` and `complete` on one connection when the request
> carries a `Signer`.

#### Barrier init proof of possession

A self-managed tenant's barrier is initialized in two calls on **one** TLS 1.3
connection:

1. **`init/begin`** (`POST /v1/tenants/{id}/barrier/init/begin`, empty body)
   is authorized like init itself and returns
   `{"nonce": "<base64, 32 bytes>", "expires": <Unix seconds>}`. The nonce is
   single-use, bound to this connection's exporter, and expires after five
   minutes. An unauthenticated deployment returns neither and asks no proof.
2. **`init/complete`** (`POST /v1/tenants/{id}/barrier/init/complete`)
   carries the usual init fields plus `nonce`, `public_key` (DER
   SubjectPublicKeyInfo) and `signature`. The signature is by the key whose
   SPKI the pending first admin records, over the tenant, the nonce and the
   connection's exporter (`pkg/initproof`).

A proof that is absent, replayed, issued on another connection, expired, or by
another key returns `FirstAdminProofError` (401 / `UNAUTHENTICATED`) and
initializes nothing; ask a fresh nonce. The SDK takes a `crypto.Signer` on
`TenantBarrierInitRequest.Signer`, never key bytes, so a device-held key signs
in place. A connection that is not TLS 1.3 has no exporter, and `begin` is
refused.

**Removed:**
- the system-tenant lift on every `ScopeSelf` permission;
- the cross-tenant `tenant_id` filter on audit query. A caller reads its own
  tenant's trail; `audit:read:cross` reads the platform trail only;
- by-id identity routing to any tenant other than the caller's;
- trust-on-first-init by "a certificate naming the tenant";
- platform `backup:*` over a strict tenant's groups.

### Reaching a strict tenant from any node

- **Set the tenant in SNI.** A client addresses a strict tenant at
  `<tenant>.<tenancy.sni_suffix>`, for example `acme.tenants.db.example.com`.
  - Any node accepts the connection.
  - A node that does not serve the tenant relays the still-encrypted
    connection to one that does, and the client's TLS session ends at the
    serving node.
  - Nothing else about the API changes. The SDK and CLI set the SNI
    themselves.
- **Unix socket and embedded** are node-local. On a node that does not serve
  the tenant, they answer `TENANT_NOT_SERVED_HERE` with the list of serving
  nodes.

### Schemas

`POST /v1/tenants` — `admin_username` and `admin_csr_pem` (shown here) or
`admin_cert_pem` are already required today, as described above
([api.md](#schemas)); `isolation_policy` is this ruling's only addition:

```json
{ "id": "acme", "name": "Acme Corp", "isolation_type": "raft",
  "isolation_policy": "strict",
  "admin_username": "alice",
  "admin_csr_pem": "-----BEGIN CERTIFICATE REQUEST-----\n..." }
```

The response for a self-managed tenant adds the serving set and the effective
policy to today's `admin` object:

```json
{ "id": "acme", "isolation_type": "raft", "barrier_mode": "self-managed",
  "effective_isolation_policy": "strict",
  "serving_set": ["node-a.db.example.com:7443", "node-b.db.example.com:7443", "node-c.db.example.com:7443"],
  "admin": { "username": "alice", "spki_thumbprint": "q3x...",
             "awaits_barrier_init": true } }
```

For a system-managed tenant, `admin.awaits_barrier_init` is absent (as today)
and the response carries the issued certificate.

`GET /v1/cluster/isolation-policy`:

```json
{ "policy": "strict", "set_at": "2026-10-01T12:00:00Z" }
```

### Status and error mapping

| Condition | REST | gRPC / Unix / QUIC / MCP |
|---|---|---|
| A strict tenant not served by this node (Unix, embedded, or no tenant SNI) | 421 + `X-QRDB-Serving-Nodes` | `UNAVAILABLE`, reason `TENANT_NOT_SERVED_HERE` — `*TenantNotServedHereError` |
| The certificate's tenant differs from the SNI tenant | TLS alert / 421 | `UNAUTHENTICATED` — `*TenantSNIMismatchError` |
| Home group without quorum, or sealed on a quorum | 503 | `UNAVAILABLE` — `*TenantHomeUnavailableError` |
| This node's home-group replica is still catching up after it joined (retry on another node) | 503 | `UNAVAILABLE` — `*TenantHomeUnavailableError` wrapping `*lifecycle.TenantHomeCatchingUpError` |
| A system identity in another tenant on a non-cross permission | 403 | `PERMISSION_DENIED` |
| `tenant_identity:*` against a strict tenant | 403 | `PERMISSION_DENIED` — `*IsolationPolicyError` |
| Loosening the floor or a tenant's policy | 409 | `FAILED_PRECONDITION` — `*IsolationPolicyChangeError` |
| Create without `admin_username`, or with neither/both of `admin_csr_pem`/`admin_cert_pem` (already shipped; see "Status and error mapping" above) | 400 | `INVALID_ARGUMENT` — `*service.TenantOnboardingFieldError`, `*bootstrap.MissingAdminCredentialError`, `*bootstrap.ConflictingAdminCredentialError` |
| Init proof of possession absent, stale or by the wrong key | 401 | `UNAUTHENTICATED` — `*FirstAdminProofError` |
| The credential does not match this node's verifier | 403 | `PERMISSION_DENIED` — `*seal.CredentialVerifierError` |
| A database replica outside the serving set | 409 | `FAILED_PRECONDITION` — `*placement.ReplicaOutsideServingSetError` |
| `strict-isolation` not yet activated cluster-wide | 409 | `FAILED_PRECONDITION` — `*featuregate.FeatureNotActiveError` |
| Creating a `barrier`, `volume` or `raft` tenant, or updating a tenant to one of those types, before `per-node-barrier-keys` is activated cluster-wide (retry after rollout). **Not raised yet:** no release advertises the feature or wires its gate, so these requests are admitted ([architecture.md](architecture.md#feature-gate-and-upgrade)) | 503 | `UNAVAILABLE` — `*featuregate.NotActiveError` |

## Pagination

`ListTenants` returns every tenant and is not paginated. `ListDeletedTenants`
is paginated as described under "Retired tenant ids".

## Versioning

Tenant REST paths are under `/v1/`. Nothing here is deprecated.
