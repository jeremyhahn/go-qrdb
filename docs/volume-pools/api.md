# Volume Pools — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-26
> Conflicts with this document are defects. Fix them here first.
>
> Names follow the existing tenant and database surfaces.

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
| Move a tenant to a pool | `POST /v1/volume-pools/{pool}/tenants/{tenant}/move` (`{pool}` is the destination; 202) | `VolumePoolMoveTenant` | `volumepool.MoveTenant` | `qrdb_volume_pool_move_tenant` | `MoveTenant` | `volume_pools:move` |
| Inspect a tenant's move | `GET /v1/tenants/{tenant}/volume-move` | `VolumePoolGetMove` | `volumepool.GetMove` | `qrdb_volume_pool_get_move` | `GetMove` | `volume_pools:read` |
| Cancel a tenant's move | `DELETE /v1/tenants/{tenant}/volume-move` | `VolumePoolCancelMove` | `volumepool.CancelMove` | `qrdb_volume_pool_cancel_move` | `CancelMove` | `volume_pools:move` |

**Every `volume_pools:*` permission is cross-scope.** It is registered with
`permission.RegisterScope(ScopeCross, …)`, so only an identity in the `system`
tenant can exercise it. The built-in Admin role carries all six, as it carries
`tenants:set_quotas`, so only Admin in the `system` tenant can use them. A
tenant's Admin cannot see the pool catalog, health or other tenants'
assignments. It sees its own tenant's pool name through `tenant get`.

**Moves.** `MoveTenant` journals a move of a `volume` or `raft` tenant to a
pool it is entitled to and answers with the journalled `VolumePoolMove`; the
move then runs node by node in the background (see
[architecture.md](architecture.md#moving-a-tenant-between-pools)). A tenant
has at most one move. `GetMove` answers the move until it ends, then
`NOT_FOUND`. `CancelMove` aborts a move that has not reached its cut over and
answers the move in state `aborting`; once cut over it is refused.

**Dropping encryption (Q9, ruling 1234).** A move from an encrypted
(`Lockable`) pool to an unencrypted one is refused unless the request carries
the operator's explicit flag:

| Transport | Request field |
|---|---|
| gRPC, Unix | `VolumePoolMoveTenantRequest.allow_unencrypted_destination = true` |
| QUIC | `allow_unencrypted_destination` in the `volumepool.MoveTenant` request |
| REST | optional JSON body `{"allowUnencryptedDestination": true}` |
| MCP | argument `allow_unencrypted_destination: true` |
| Go SDK | `transport.VolumePoolMoveTenantRequest.AllowUnencryptedDestination` |
| TypeScript SDK | `moveTenant(tenant, toPool, { allowUnencryptedDestination: true })` |

A move admitted under the flag that drops encryption answers with
`"encryptionDropped": true` (proto `encryption_dropped`). The drop is audited
fail-closed (`volume_pool.move_encryption_drop`) before the move is admitted,
and a server with no audit sink refuses it. Every `volume_pool.move_start`
record carries `allow_unencrypted_destination` and `encryption_dropped`.

## Volume pool fields on the tenant surface

| Message / route | Field |
|---|---|
| `TenantCreateRequest` / `POST /v1/tenants` | `volume_pool` (string, field 6): the pool a `volume` or `raft` tenant is placed on. When empty, the tenant's default pool (the first entry of `allowed_volume_pools`) is used, and with no entitled pool the create is refused with `PERMISSION_DENIED` (`volume_pool:<none>`). Refused for `prefix` and `barrier`; a `barrier` tenant is placed by the registry, and the tenant's `volume_pool` reports the pool it chose. Field 5 is `reserved`. |
| `TenantResponse` | `volume_pool` (string, field 9) and `refusal` (`TenantRefusal`, field 10, absent when the answering node serves the tenant). Field 8 is `reserved`. |
| `TenantEntitlements` | `allowed_volume_pools` (repeated string). |
| Deleted-tenant records (`TenantListDeleted`) | `volume_pool`. |
| MCP `qrdb_tenant_create` | the `volume_pool` argument. |
| CLI `tenant create` | `--pool` (see [cli.md](cli.md)). |
| Go SDK (`sdk/go`) | `transport.CreateTenantRequest.VolumePool`, `transport.TenantInfo.VolumePool` and `.Refusal *TenantRefusal`, `transport.DeletedTenant.VolumePool`. |
| TypeScript SDK | `volumePool` on the create request and the tenant type, and `refusal` on the tenant type. |
| GUI | a Pool selector on the create form; a refused tenant shows its refusal. |

```protobuf
message TenantRefusal {
    // code is stable: pool_unavailable | provider_missing.
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
  "usedBytes": 53687091200, "tenantCount": 3, "overcommit": 1.0,
  "nodes": { "ready": 3, "locked": 0, "unavailable": 0, "unmapped": 0 } }

// VolumePoolDetail (inspect): a VolumePool plus one row per node
{ "...": "...",
  "nodeReports": [
    { "nodeId": "node-1", "state": "ready",
      "sizeBytes": 3840755982336, "freeBytes": 3700000000000,
      "detail": "", "reportedAt": "<RFC 3339 time>" } ] }

// CreateVolumePoolRequest
{ "name": "fast", "provider": "dir", "description": "NVMe tier",
  "capacityBytes": 0, "overcommit": 1.0 }

// UpdateVolumePoolRequest: absent fields are unchanged; provider is immutable
{ "description": "NVMe tier, rack A", "capacityBytes": 2199023255552 }

// VolumePoolTenant (list-tenants row; paginated with page_token / page_size)
{ "tenantId": "acme", "reservedBytes": 214748364800 }

// VolumePoolMove (the move-tenant, get-move and cancel-move answer)
{ "tenantId": "acme", "fromPool": "default", "toPool": "fast",
  "state": "copying", "nodesCopied": ["node-1"], "nodesPending": ["node-2"],
  "stepNode": "node-2", "stepAttempts": 1, "error": "",
  "reservedBytes": 214748364800, "nodesRetired": [],
  "sourceReservedBytes": 214748364800, "tenantRecorded": false,
  "admittedAt": "<RFC 3339 time>", "updatedAt": "<RFC 3339 time>" }
// "encryptionDropped": true is present only on a move that drops encryption
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
| No move journalled for the tenant (get-move, cancel-move) | 404 | `NOT_FOUND` | `StatusNotFound` | `NOT_FOUND` |
| Move conflict: a move already journalled, a tenant that is not `volume` or `raft`, or a cancel past the cut over | 409 | `FAILED_PRECONDITION` | `StatusFailedPrecondition` | `FAILED_PRECONDITION` |
| Move from an encrypted pool to an unencrypted one without `allow_unencrypted_destination`, or under it with no audit sink | 409 | `FAILED_PRECONDITION` | `StatusFailedPrecondition` | `FAILED_PRECONDITION` |
| The move journal changed under every compare-and-swap | 409 | `ABORTED` | `StatusAborted` | `ABORTED` |
| Reservation conflict after bounded retries | 409 | `ABORTED` | `StatusAborted` | `ABORTED` |
| Pool unavailable on the answering node (includes a `Lockable` volume that failed to unlock, or a pool this node skipped at boot under `--allow-degraded-volume-boot`) | 503 | `UNAVAILABLE` | `StatusUnavailable` | `UNAVAILABLE` |

The typed errors behind each row are listed in
[engineering.md](engineering.md#error-semantics). The embedded transport
returns them as is.

## Versioning

All routes are `v1`. Field numbers 5 and 8 are `reserved`, so an old client's
`data_root` is never reinterpreted.
