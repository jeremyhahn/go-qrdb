# etcd Compatibility — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

> **Build-tag gated: `qrdb_etcd`** (`WITH_QRDB_ETCD=1`, on by default). Disable
> with `WITH_QRDB_ETCD=0`.

## Configuration

etcd compatibility is configured **per database**, not globally:

etcd compatibility is configured per-database in the database configuration:

```yaml
databases:
  - name: k8s-prod
    engine: kv
    backend: raft
    protocol:
      etcd:
        enabled: true
        listen_addr: ":2379"
        advertise_client_urls:
          - "https://node1:2379"
```

**Validation:** TLS and auth must be enabled when the etcd protocol is active. The database `engine` must be `kv` and `backend` must be `raft`.


**Validation is strict and fails startup, not the first request:**

- TLS and auth must both be enabled when the etcd protocol is active.
- The database `engine` must be `kv`.
- The database `backend` must be `raft`.
- For Kubernetes, `sm_type` must additionally be `mvcc` — Kubernetes needs
  revisions, watches, leases and transactions.

## RPC mapping

The etcd gRPC services map to QRDB service layer calls:

| etcd service | RPC | Backed by | Permission |
|---|---|---|---|
| `KV` | `Range` | `MVCCKVService.Range` / `GetRevision` | `kv:range` |
| `KV` | `Put` | `MVCCKVService.PutWithLease` | `kv:put` |
| `KV` | `DeleteRange` | `MVCCKVService.Delete` | `kv:delete` |
| `KV` | `Txn` | `TransactionService.Execute` | `transaction:execute` |
| `KV` | `Compact` | `MVCCKVService.Compact` | `mvcc:compact` (system tenant only) |
| `Watch` | `Watch` | `WatchService.Watch` / `WatchPrefix` | `watch:watch_prefix` |
| `Lease` | `LeaseGrant` | `LeaseService.Grant` | `lease:grant` |
| `Lease` | `LeaseRevoke` | `LeaseService.Revoke` | `lease:revoke` |
| `Lease` | `LeaseKeepAlive` | `LeaseService.KeepAlive` | `lease:keepalive` |
| `Lease` | `LeaseTimeToLive` | `LeaseService.TimeToLive` | `lease:ttl` |
| `Lease` | `LeaseLeases` | `LeaseService.Leases` | `lease:list` |
| `Cluster` | `MemberList` | `AdminService.Peers` | `admin:peers` |
| `Maintenance` | `Status` | current revision | `admin:status` |
| `Maintenance` | `Alarm` | — | `admin:status`; returns no alarms |
| every other RPC — `Auth.*`, member changes, `Defragment`, `Snapshot`, `Hash`, `HashKV`, `MoveLeader`, `Downgrade` | | — | **refused** with `PERMISSION_DENIED` before any handler runs |

The `translate.go` module handles bidirectional type conversion between etcd protobuf types (`mvccpb.KeyValue`, `etcdserverpb.RangeRequest`, etc.) and QRDB service types (`service.KeyValue`, `service.RangeOptions`, etc.).

`translate.go` performs bidirectional type conversion between etcd protobuf
types (`mvccpb.KeyValue`, `etcdserverpb.RangeRequest`, …) and qrdb service
types (`service.KeyValue`, `service.RangeOptions`, …).

## Invariants

- **A listener serves exactly one database.** `startEtcd` resolves the
  database's MVCC, transaction, watch and lease services through the
  per-(tenant, database) service registry for the database's owning tenant; it
  is never handed the raw shard. An engine the database does not serve is left
  unregistered; any other resolution error refuses the start.
- **Every RPC is authenticated and authorized.** `etcd.RequestAuthorizer` runs
  as a unary and a stream interceptor: the node's authenticator chain, a
  refusal for a caller from another (non-system) tenant, the RPC's permission
  for the owning tenant, and a fresh registry resolution so a sealed or dropped
  database is refused on the next request.
- **The RPC-to-permission map is default-deny** (`etcd.MethodPermission`). An RPC
  without a row — the `Auth` service, member changes, `Defragment`, `Snapshot`
  and the other maintenance verbs — is refused before its handler runs.
- **The listener refuses to start without TLS and an authorizer**
  (`etcd.AuthRequiredError`), and the composer refuses without a database
  catalog or service registry (`etcd.ScopeUnavailableError`).
- **`Compact` is system-tenant only on the shared shard.** Revisions there are
  store-global, so the namespaced MVCC service refuses a non-system caller.

## Error semantics

| Condition | Behavior | Fails closed? |
|---|---|---|
| Built without `qrdb_etcd` | listener never starts | **yes** |
| `engine` is not `kv` | create or etcd update refused: `database.EtcdEngineUnsupportedError` (`INVALID_ARGUMENT`) | **yes** |
| Node in mode `standalone` | listener refused: `service.CapabilityUnsupportedError` ("requires mode: raft") | **yes** |
| TLS or the node's authentication not configured | listener refused: `etcd.AuthRequiredError` | **yes** |
| No database catalog or service registry to confine the listener | listener refused: `etcd.ScopeUnavailableError` | **yes** |
| Client presents no certificate | `UNAUTHENTICATED` on every RPC | **yes** |
| Caller from another tenant | `PERMISSION_DENIED` with the generic `etcd.PermissionDeniedError` text; the detail (`etcd.TenantMismatchError`) is logged server-side only | **yes** |
| Any RPC without a permission mapping, including `Auth.*` | `PERMISSION_DENIED` | **yes** — no fallback to password auth |
| Database sealed or dropped after the listener started | `UNAVAILABLE` / `NOT_FOUND` on the next RPC or stream | **yes**, for new RPCs and streams; see the stream limit below |
| Missing permission for the mapped operation | permission denied on the RPC | **yes** |
| Compacted revision requested | etcd's compacted-revision error | **yes** |
| Barrier sealed | requests refused | **yes** |

**Known limit: streams are authorized when they open.** Authentication, the
tenant check, the RPC's permission and the database resolution run once when a
`Watch` or `LeaseKeepAlive` stream opens, not per message. A stream already
open when its database is sealed or dropped, or when the caller's role loses
the permission, keeps running until the client or the server ends it: a
`Watch` keeps delivering events for the keys it watches, and a
`LeaseKeepAlive` keeps renewing leases the database owns. Every new RPC and
every new stream is refused. To cut existing streams, restart the listener's
node or close the client connections. The watch streams of the other
transports (gRPC `Watch`, REST watch) resolve their database once when they
open in the same way.

Typed errors live in `pkg/api/etcd/errors.go`; configuration validation in
`config.go`.

## Package structure

```
pkg/api/etcd/
  server.go        Per-database etcd gRPC server lifecycle
  kv.go            KVServer (Range, Put, DeleteRange, Txn, Compact)
  watch.go         WatchServer with stream multiplexing
  lease.go         LeaseServer (Grant, Revoke, KeepAlive, TTL, Leases)
  auth.go          AuthServer stub (mTLS instead)
  cluster.go       ClusterServer (MemberList)
  maintenance.go   MaintenanceServer (Status, Defrag, Alarm)
  translate.go     Type translation: qrdb <-> etcd protobuf
  config.go        Config validation
  errors.go        Typed errors
```

## Test strategy

```bash
make test-etcd            # etcd API tests
make coverage-etcd        # etcd coverage (91%+ required)
```

The 91% coverage floor is enforced by the target, not by convention.

- **Unit**: per-RPC translation round-trips, authorization mapping for each
  operation in the table and default-deny for every other RPC, the refusal to
  start without authentication or a scope, and confinement: a listener resolved
  through the service registry returns only its database's keys from a store
  shared with other tenants and databases, refuses another tenant's caller and
  an unauthenticated one, and refuses the next RPC once the database is sealed.
- **Integration**: real `etcdctl` and `clientv3` flows in the standalone stage.
  Multi-node etcd behaviour belongs to the cluster stage, which is **not
  currently wired into CI**.

## Observability

- `Maintenance.Status` reports through the qrdb admin service, so an etcd client
  sees the real node status rather than a synthesised one.
- `Maintenance.Alarm` returns the current alarm state.
- etcd operations are audited through [../audit/](../audit/) like any other
  data-plane access.
