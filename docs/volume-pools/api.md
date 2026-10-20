# Volume Pools — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06
> Conflicts with this document are defects. Fix them here first. Decisions
> D1-D10 are ruled; the Phase 1 build is in progress.
>
> Names follow the existing tenant and database surfaces; they are the
> contract the implementation builds to. The tenant-move rows (`TenantMove`,
> `TenantMoveStatus` and their REST/QUIC/MCP/embedded equivalents) are
> **Phase 2** (decision D5) -- every other row here is Phase 1.

Volume pool management is **Tier 1**: every capability is on all six
transports. Pool management runs on `qrdb.v1.QRDBService`, beside the tenant
RPCs. The Unix socket serves the same gRPC wire over a UDS.

## Capability matrix — pool management

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded (`Client.VolumePools()`) | `scp` permission |
|---|---|---|---|---|---|---|
| List pools | `GET /v1/volume-pools/list` | `VolumePoolList` | `volumepool.List` | `qrdb_volume_pool_list` | `ListPools` | `volume_pools:list` |
| Inspect a pool | `GET /v1/volume-pools/{pool}` | `VolumePoolGet` | `volumepool.Get` | `qrdb_volume_pool_get` | `GetPool` | `volume_pools:read` |
| Create | `POST /v1/volume-pools` | `VolumePoolCreate` | `volumepool.Create` | `qrdb_volume_pool_create` | `CreatePool` | `volume_pools:create` |
| Update | `PUT /v1/volume-pools/{pool}` | `VolumePoolUpdate` | `volumepool.Update` | `qrdb_volume_pool_update` | `UpdatePool` | `volume_pools:update` |
| Delete | `DELETE /v1/volume-pools/{pool}` | `VolumePoolDelete` | `volumepool.Delete` | `qrdb_volume_pool_delete` | `DeletePool` | `volume_pools:delete` |
| List a pool's tenants | `GET /v1/volume-pools/{pool}/tenants` | `VolumePoolListTenants` | `volumepool.ListTenants` | `qrdb_volume_pool_list_tenants` | `ListPoolTenants` | `volume_pools:read` |
| Move a tenant *(Phase 2, D5)* | `POST /v1/tenants/{id}/move` | `TenantMove` | `tenant.Move` | `qrdb_tenant_move` | `Client.Tenants().MoveTenant` | `volume_pools:move` |
| Move status *(Phase 2, D5)* | `GET /v1/tenants/{id}/move` | `TenantMoveStatus` | `tenant.MoveStatus` | `qrdb_tenant_move_status` | `Client.Tenants().GetTenantMove` | `volume_pools:move` |

**Every `volume_pools:*` permission is cross-scope.** It is registered with
`permission.RegisterScope(ScopeCross, …)`, so only an identity in the `system`
tenant can exercise it. The built-in Admin role carries all six, as it carries
`tenants:set_quotas`, so only Admin in the `system` tenant can use them. A
tenant's Admin cannot see the pool catalog, health or other tenants'
assignments. It sees its own tenant's pool name through `tenant get`.

The tenant-move rows live on the tenant surface because a move acts on one
tenant. They are gated by the pool permission because the move changes where
the tenant's data lives on every node, which is a platform act.

## Changes to the tenant surface — BREAKING

| Message / route | Change |
|---|---|
| `TenantCreateRequest` / `POST /v1/tenants` | `data_root` (field 5) is **removed** and its number reserved. Adds `volume_pool` (string): the pool a `volume` or `raft` tenant is placed on. When empty, the tenant's default pool (the first entry of `allowed_volume_pools`) is used, and with no entitled pool the create is refused with `PERMISSION_DENIED` (`volume_pool:<none>`). Refused for `prefix` and `barrier`; a `barrier` tenant is placed by the registry, and the tenant's `volume_pool` reports the pool it chose. |
| `TenantResponse` | `data_root` (field 8) is **removed** and its number reserved. Adds `volume_pool` (string) and `refusal` (`TenantRefusal`, absent when the answering node serves the tenant). |
| `TenantEntitlements` | adds `allowed_volume_pools` (repeated string). |
| Deleted-tenant records (`TenantListDeleted`) | `data_root` is **removed**; adds `volume_pool`. |
| MCP `qrdb_tenant_create` | the `data_root` argument is removed; adds `volume_pool`. |
| CLI `tenant create --data-root` | removed. `--pool` is added (see [cli.md](cli.md)). |
| Go SDK (`sdk/go`) | `transport.CreateTenantRequest.DataRoot`, `transport.TenantResponse.DataRoot` and `DeletedTenant.DataRoot` are removed; `VolumePool` and `Refusal *TenantRefusal` are added. |
| TypeScript SDK | `dataRoot` is removed from the create request and the tenant type; `volumePool` and `refusal` are added. |
| GUI | the Data Root field is removed from the create form; a Pool selector is added, and a refused tenant shows its refusal. |

```protobuf
message TenantRefusal {
    // code is stable: persisted_path | pool_unavailable | provider_missing.
    string code    = 1;
    // message is the typed error's text; for display, never parsed.
    string message = 2;
}
```

The refusal is **the answering node's view**. Another node may serve the
tenant, so a client that needs the cluster view reads the pool's per-node
health.

## Schemas

```json
// VolumePool (list row)
{ "name": "fast", "provider": "dir", "description": "NVMe tier",
  "capacityBytes": 2199023255552, "reservedBytes": 214748364800,
  "usedBytes": 53687091200, "tenantCount": 3,
  "nodes": { "ready": 3, "locked": 0, "unavailable": 0, "unmapped": 0 } }

// VolumePoolDetail (inspect): a VolumePool plus one row per node
{ "...": "...",
  "nodeReports": [
    { "nodeId": "node-1", "mapped": true, "state": "ready",
      "sizeBytes": 3840755982336, "freeBytes": 3700000000000,
      "detail": "", "reportedAt": "2026-10-04T12:00:00Z" } ] }

// CreateVolumePoolRequest
{ "name": "fast", "provider": "dir", "description": "NVMe tier",
  "capacityBytes": 0, "overcommit": 1.0 }

// UpdateVolumePoolRequest: absent fields are unchanged; provider is immutable
{ "description": "NVMe tier, rack A", "capacityBytes": 2199023255552 }

// VolumePoolTenant (list-tenants row; paginated with pageToken / pageSize)
{ "tenantId": "acme", "reservedBytes": 214748364800, "usedBytes": 53687091200 }

// TenantMoveRequest / TenantMove -- Phase 2 (decision D5)
{ "toPool": "bulk" }
{ "tenantId": "acme", "fromPool": "fast", "toPool": "bulk",
  "state": "copying", "nodesCopied": ["node-1"], "nodesPending": ["node-2", "node-3"],
  "error": "" }
```

No response carries a node mount or device. The per-node rows report state and
numbers only.

**Pagination.** `ListPoolTenants` is unbounded and pages with `page_token` and
`page_size`, under the same rules as `TenantListDeleted`. `ListPools` is not
paginated, because a cluster has a handful of pools.

## Status mapping

| Condition | REST | gRPC | QUIC | MCP |
|---|---|---|---|---|
| Unknown pool | 404 | `NOT_FOUND` | `StatusNotFound` | `NOT_FOUND` |
| Pool name taken | 409 | `ALREADY_EXISTS` | `StatusAlreadyExists` | `ALREADY_EXISTS` |
| Delete with tenants assigned | 409 | `FAILED_PRECONDITION` | `StatusFailedPrecondition` | `FAILED_PRECONDITION` |
| Create names a provider this node lacks | 409 | `FAILED_PRECONDITION` | `StatusFailedPrecondition` | `FAILED_PRECONDITION` |
| Reservation exceeds capacity | 429 | `RESOURCE_EXHAUSTED` | `StatusResourceExhausted` | `RESOURCE_EXHAUSTED` |
| Tenant not entitled to the pool | 403 | `PERMISSION_DENIED` | `StatusPermissionDenied` | `PERMISSION_DENIED` |
| Move or delete during a move *(Phase 2, D5)* | 409 | `FAILED_PRECONDITION` | `StatusFailedPrecondition` | `FAILED_PRECONDITION` |
| Reservation conflict after bounded retries | 409 | `ABORTED` | `StatusAborted` | `ABORTED` |
| Pool unavailable on the answering node (includes a `Lockable` volume that failed to unlock, decision D6, or a pool this node skipped at boot under `--allow-degraded-volume-boot`, decision D10) | 503 | `UNAVAILABLE` | `StatusUnavailable` | `UNAVAILABLE` |
| A move step failed *(Phase 2, D5)* | 500 | `INTERNAL` | `StatusError` | `INTERNAL` |

The typed errors behind each row are listed in
[engineering.md](engineering.md#error-semantics). The embedded transport
returns them as is.

## Removed: the in-process volume pool client

`Client.VolumePools()` currently returns the in-process `VolumePoolClient`
(`List`, `Pick`, `RegisterPool`), documented in
[storage-engines](../storage-engines/api.md#volume-pools). It is **replaced** by
the management client in the matrix above (decision D8).

- `Pick` is removed: it returned a node path.
- `RegisterPool` is removed: pools are created in the catalog, and a node maps
  them in its configuration.

The storage-engines folder is updated in the same change.

## Versioning

All routes are `v1`. The removals are BREAKING and ship in one minor release,
with a CHANGELOG entry under `Changed — BREAKING`. Field numbers 5 and 8 are
`reserved`, so an old client's `data_root` is never reinterpreted.
