# Distributed Locks — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

gRPC service `qrdb.v1.QRDBService`. The Unix socket transport serves the same
gRPC wire over a UDS, so its methods match the gRPC column.

## Capability matrix

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded | `scp` permission |
|---|---|---|---|---|---|---|
| Acquire (blocking) | `POST /lock/acquire` | `LockAcquire` | `lock.Acquire` | `qrdb_lock_acquire` | `Client.Lock().LockAcquire` | `locks:acquire` |
| TryLock (non-blocking) | `POST /lock/trylock` | `LockTryLock` | `lock.TryLock` | `qrdb_lock_trylock` | `Client.Lock().LockTryLock` | `locks:acquire` |
| Release | `POST /lock/release` | `LockRelease` | `lock.Release` | `qrdb_lock_release` | `Client.Lock().LockRelease` | `locks:release` |
| Extend | `POST /lock/extend` | `LockExtend` | `lock.Extend` | `qrdb_lock_extend` | `Client.Lock().LockExtend` | `locks:extend` |
| Status | `GET /lock/status` | `LockStatus` | `lock.Status` | `qrdb_lock_status` | `Client.Lock().LockStatus` | `locks:status` |
| List | `GET /lock/list` | `LockList` | `lock.List` | `qrdb_lock_list` | `Client.Lock().LockList` | `locks:list` |

`trylock` deliberately shares the `locks:acquire` permission with `acquire` —
blocking is a client-side waiting strategy, not a distinct privilege.

**REST paths are unversioned.** `docs/18-distributed-locking.md` documents them
as `/v1/lock/*`; that is stale. The code registers `/lock/*` and the code is
correct.

## Schemas

`LockAcquireRequest` (acquire and trylock):

```json
{ "key": "jobs/nightly-report", "holder_id": "worker-7", "ttl": 300, "reentrant": false }
```

`holder_id` is optional; when empty the server generates a UUID. It is a
reentrancy discriminator, **not an authenticated identity**.

`LockInfo` (returned by acquire, trylock, extend, status; the elements of list):

```json
{
  "key": "jobs/nightly-report",
  "fencing_token": 4711,
  "lease_id": 88,
  "holder_id": "worker-7",
  "reentrant": false,
  "acquire_count": 1,
  "ttl": 300,
  "acquired_at": 1755561600
}
```

`fencing_token` is the MVCC `ModRevision` at acquisition — monotonically
increasing across successive acquisitions of the same lock. Pass it to the
protected resource and have the resource reject anything lower than the highest
it has seen. `key` is the user key, with the internal `__sys:lock/` prefix
stripped.

`LockReleaseRequest` and `LockExtendRequest`: `{ "key": ..., "fencing_token": ... }`.

`LockListResponse`: `{ "locks": [LockInfo, ...] }`. REST `GET /lock/list` takes
an optional `prefix` query parameter.

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Acquired | 200 | `OK` |
| `trylock` on a held lock | 409 | `FAILED_PRECONDITION` |
| `acquire` wait timeout exceeded | 408 | `DEADLINE_EXCEEDED` |
| Stale or wrong fencing token on release/extend | 409 | `FAILED_PRECONDITION` — `NotLockHolderError` |
| Lock not held / not found | 404 | `NOT_FOUND` |
| `ttl <= 0` | 400 | `INVALID_ARGUMENT` |
| Missing permission | 403 | `PERMISSION_DENIED` |
| Barrier sealed | 503 | `UNAVAILABLE` |
| Not leader, no resolver | 503 | `UNAVAILABLE` |

No failure path returns a `LockInfo`. A caller that gets a lock got the lock.

## Pagination

`LockList` returns every matching lock and is not paginated; `prefix` is the
narrowing mechanism.

## Versioning

Lock REST paths are unversioned (`/lock/...`). Nothing here is deprecated.
