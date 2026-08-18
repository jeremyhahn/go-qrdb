# Leases — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

gRPC service `qrdb.v1.QRDBService`. The Unix socket transport serves the same
gRPC wire over a UDS, so its methods match the gRPC column.

## Capability matrix

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded | `scp` permission |
|---|---|---|---|---|---|---|
| Grant | `POST /lease/grant` | `LeaseGrant` | `lease.Grant` | `qrdb_lease_grant` | `Client.Lease().LeaseGrant` | `lease:grant` |
| Revoke | `POST /lease/revoke` | `LeaseRevoke` | `lease.Revoke` | `qrdb_lease_revoke` | `Client.Lease().LeaseRevoke` | `lease:revoke` |
| Keep-alive | `POST /lease/keepalive` | `LeaseKeepAlive` | `lease.KeepAlive` | `qrdb_lease_keepalive` | `Client.Lease().LeaseKeepAlive` | `lease:keepalive` |
| Time-to-live | `GET /lease/ttl` | `LeaseTimeToLive` | `lease.TimeToLive` | `qrdb_lease_ttl` | `Client.Lease().LeaseTimeToLive` | `lease:ttl` |
| List | `GET /lease/list` | `LeaseList` | `lease.List` | `qrdb_lease_list` | `Client.Lease().LeaseList` | `lease:list` |
| Attach a key to a lease | `PUT /kv/{key}` with a lease field | `PutWithLease` | `mvcc.PutWithLease` | `qrdb_mvcc_put_with_lease` | `Client.MVCC().PutWithLease` | `mvcc:put_with_lease` |

Attachment is a KV capability rather than a lease one; it is listed here because
a reader looking for "how do I use a lease" needs it, and cross-referenced from
[../key-value/api.md](../key-value/api.md).

## Schemas

`LeaseInfo`, returned by grant, keep-alive and time-to-live, and in the list:

```json
{
  "id": 7,
  "ttl": 60,
  "granted_ttl": 60,
  "remaining_ttl": 41,
  "keys": ["c2Vzc2lvbi9hYmM="]
}
```

`granted_ttl` is fixed at grant time; `remaining_ttl` counts down and is reset to
`granted_ttl` by keep-alive. `keys` is base64 and is present where the store
tracks attachments.

Envelopes: `LeaseGrantResponse`, `LeaseKeepAliveResponse`,
`LeaseTimeToLiveResponse` each carry a single `lease`; `LeaseListResponse`
carries `leases`.

`LeaseRevoke` returns `google.protobuf.Empty` on gRPC and an empty body on REST.

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success | 200 | `OK` |
| Lease not found (expired, revoked, never existed) | 404 | `NOT_FOUND` |
| `ttl <= 0` on grant | 400 | `INVALID_ARGUMENT` |
| Write attaching to an unknown lease | 400 | `INVALID_ARGUMENT` — the write is refused |
| Missing permission | 403 | `PERMISSION_DENIED` |
| Barrier sealed | 503 | `UNAVAILABLE` |
| Not leader, no resolver | 503 | `UNAVAILABLE` |

## Pagination

`LeaseList` returns every active lease in scope and is not paginated. The set is
bounded by what the tenant has granted; a caller that expects an unbounded
number of leases is misusing the primitive.

## Versioning

Lease REST paths are unversioned (`/lease/...`). Nothing here is deprecated.
