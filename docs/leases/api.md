# Leases — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-13
> Conflicts with this document are defects. Fix them here first.

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

**Revisions in the attach response depend on the state machine.** On a state
machine with MVCC revision history (`sm_type: mvcc`) the response carries the
write's `create_revision`, `mod_revision` and `version`. The default `pebble`
state machine attaches the lease and stores the value the same way but keeps no
revision history, so on every transport the response carries the key, value and
lease with all three revisions `0`. A client that needs revisions runs the
`mvcc` state machine; the etcd API refuses to serve without one.

Attachment follows the key's latest write. A put naming lease B moves a key out
of the lease it was in. A put naming no lease (lease `0`, or any plain `Put`)
releases it, and so does a delete. Revoking or expiring a lease deletes only the
keys still attached to it, and only those appear in its `keys`. A
compare-and-swap keeps the key's current lease. Every transport behaves the same
way, because the binding is decided in the state machine.

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

Two timing facts hold on every transport. A keep-alive is answered by the leader
from its memory and is not replicated, so another replica's deadline does not
reflect it. `remaining_ttl` is therefore the leader's answer, as in etcd: a
time-to-live sent to a follower is forwarded to the leader, and the response
is the leader's. A follower that cannot name a leader answers the not-leader
refusal (`503` on REST, `Unavailable` on gRPC). The etcd listener does not
forward and answers `Unavailable`, which clientv3 retries on another endpoint. Only the leader's value decides
expiry. Every
`lease.checkpoint_interval` (default `5m`) the leader records the remaining TTL
of each lease whose TTL is longer than that. After a leader change, the new
leader reports a `remaining_ttl` of at least that recorded value — or
`granted_ttl` for a lease with none — plus `lease.failover_grace` (default
`5s`), so a `remaining_ttl` larger than `granted_ttl` can be seen then. Neither
changes the wire format or the status mapping below; a keep-alive on a replica
that is not the leader is forwarded and answered the same way.

Envelopes: `LeaseGrantResponse`, `LeaseKeepAliveResponse`,
`LeaseTimeToLiveResponse` each carry a single `lease`; `LeaseListResponse`
carries `leases`.

`LeaseRevoke` returns `google.protobuf.Empty` on gRPC and an empty body on REST.

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success | 200 | `OK` |
| Lease not found (expired, revoked, never existed) | 404 | `NOT_FOUND` |
| `ttl <= 0` on grant | 400 | `INVALID_ARGUMENT` — `service.CommandRejectedError` |
| Grant on a state machine built without a lessor — only the legacy `regular` state machine, which `qrdbd` refuses to start on | 501 | `UNIMPLEMENTED` — `service.CapabilityUnsupportedError`, message `statemachine: lease not configured` |
| Lessor closed on this replica | 503 | `UNAVAILABLE` — `service.SubsystemClosedError` |
| Grant whose lease ID is already held | 409 | `ALREADY_EXISTS` — `service.CommandConflictError` |
| Any other lessor failure during apply | 500 | `INTERNAL` — `service.ApplyFaultError` |
| Write attaching to an unknown lease | 404 | `NOT_FOUND` — `service.LeaseNotFoundError`; the write is refused and nothing is written, whether the namespaced view caught it before proposal or the state machine caught it at apply *(corrected: this row said 400 / `INVALID_ARGUMENT`, which no path returned)* |
| Missing permission | 403 | `PERMISSION_DENIED` |
| Barrier sealed | 503 | `UNAVAILABLE` |
| Not leader, no resolver | 503 | `UNAVAILABLE` |

The etcd API (`etcdserverpb.Lease`) answers a keep-alive on a lease that is
not found as etcd does instead of failing the stream: a
`LeaseKeepAliveResponse` carrying the lease ID and `TTL: 0`, with the stream
left open. etcd clients read that as "lease gone".

## Pagination

`LeaseList` returns every active lease in scope and is not paginated. The set is
bounded by what the tenant has granted; a caller that expects an unbounded
number of leases is misusing the primitive.

## Versioning

Lease REST paths are unversioned (`/lease/...`). Nothing here is deprecated.
