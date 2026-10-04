# etcd Compatibility — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-01

> **Build-tag gated: `qrdb_etcd`** (`WITH_QRDB_ETCD=1`, on by default). Disable
> with `WITH_QRDB_ETCD=0`.

## Configuration

etcd compatibility is configured **per database**, not globally:

etcd compatibility is configured per-database in the database configuration.
`etcd` sits directly on the database entry — flat, the same as `mysql` and
`postgresql` — not nested under a `protocol:` key: `config.DatabaseConfig`
has no `protocol` field, and a nested block here is silently dropped by the
YAML decoder rather than applied (corrected 2026-10-03; an earlier version
of this example showed a nested `protocol.etcd` shape that never matched
what the loader reads).

```yaml
databases:
  - name: k8s-prod
    engine: kv
    backend: raft
    etcd:
      enabled: true
      listen_addr: ":2379"
      advertise_client_urls:
        - "https://node1:2379"
```

**Validation:** TLS and auth must be enabled when the etcd protocol is active, the node must run in mode `raft`, and the database `engine` must be `kv` -- any `backend` serves it.


**Validation is strict and fails startup, not the first request:**

- TLS and auth must both be enabled when the etcd protocol is active.
- The node must run in mode `raft`: the etcd listener is a raft-only surface
  (`capabilities.Etcd`), and a standalone node's listener refuses to start.
- The database `engine` must be `kv`; enabling etcd on any other engine is
  refused with `database.EtcdEngineUnsupportedError`. Any `backend` serves
  it -- the listener resolves the database through the service registry
  regardless of backend. *(Corrected 2026-10-03: `backend: raft` was
  documented as required here and was neither required nor checked.)*
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
| `KV` | `Compact` | `MVCCKVService.Compact` (this database only) | `mvcc:compact` |
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
- **`Compact` compacts this database's history only.** The namespaced MVCC
  service compacts the database's own `<tenant>/<database>/` prefix (its whole
  shard under raft isolation); revisions stay store-global on the shared shard,
  and the compaction floor is per database. The owning tenant's identities
  holding `mvcc:compact` may compact; no other tenant may, the system tenant
  included.
- **`Compact` answers with etcd's revision errors.** A revision the database is
  already compacted to is `rpctypes.ErrGRPCCompacted`, one above the current
  revision `rpctypes.ErrGRPCFutureRev` (both `OUT_OF_RANGE`, etcd's own
  messages), so `kube-apiserver`'s compactor reads them exactly as it would
  from etcd.
- **A `Watch` from a compacted revision is etcd's compacted watch.** A start
  revision at or below the database's compaction floor is answered as etcd
  answers it: the watch is created, then cancelled by a response whose
  `CompactRevision` is the floor, which an etcd client reports as
  `rpctypes.ErrCompacted` -- `kube-apiserver`'s reflector relists on it. The
  same response ends a watch whose replay compaction overtook. The floor is
  the watched range's own (`watch.CompactedError`), never another database's.
  *(Corrected: the create was refused with a response an etcd client cannot
  match to its request, so the client's `Watch` call never returned.)*
- **A refused create is etcd's failed create.** Any other refusal -- an empty
  key, a permission or scope refusal from the watch service -- is `Created` and
  `Canceled` with watch ID `-1` and the reason, which the client returns as the
  watch's error; a watch that ends for a reason (an overflowed consumer, the
  database ceasing to serve) is cancelled with that reason rather than going
  silent.

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
| Database sealed or dropped after the listener started | `UNAVAILABLE` / `NOT_FOUND` on the next RPC, and open streams end with it | **yes** |
| Missing permission for the mapped operation | permission denied on the RPC | **yes** |
| Compacted revision requested | etcd's compacted-revision error; a `Watch` is created then cancelled with `CompactRevision` set to the floor | **yes** |
| `Compact` to a revision already compacted, or above the current one | `ErrGRPCCompacted` / `ErrGRPCFutureRev`; nothing changes | **yes** |
| Barrier sealed | requests refused | **yes** |
| The state machine keeps no MVCC revision history (the default `pebble`) | the server refuses to start (`RevisionHistoryRequiredError`); a `Put` that reaches one is `FailedPrecondition`, never revision 0 | **yes** |

**Streams end when their database stops serving.** A `Watch` or
`LeaseKeepAlive` stream is guarded by its database (`etcd.StreamGuard`): the
moment the database is sealed or dropped, the stream ends with the sealed
refusal (`UNAVAILABLE`) or the dropped database's `NOT_FOUND`, even when the
client sends nothing more. The watch streams of the other transports (gRPC
and Unix `Watch`, QUIC, REST) end the same way, because every watch on a
catalogued database is opened through the service registry, which ties it to
the database. *(Corrected: a stream used to run on over a sealed or dropped
database until the client ended it.)* Authentication and the RPC's permission
are decided when a stream opens: a change to the caller's roles applies to its
next stream, not to one already open.

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
