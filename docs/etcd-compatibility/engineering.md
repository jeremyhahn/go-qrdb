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

| etcd gRPC Service | etcd RPC | QRDB Service | QRDB Method |
|---|---|---|---|
| `KV` | `Range` | `MVCCKVService` | `Range(ctx, key, rangeEnd, opts)` |
| `KV` | `Put` | `MVCCKVService` | `Put(ctx, key, value)` |
| `KV` | `DeleteRange` | `MVCCKVService` | `DeleteRange(ctx, key, rangeEnd)` |
| `KV` | `Txn` | `TransactionService` | `Execute(ctx, compares, successOps, failureOps)` |
| `KV` | `Compact` | `MVCCKVService` | `Compact(ctx, revision)` |
| `Watch` | `Watch` | `WatchService` | `Watch(ctx, key, opts)` / `WatchPrefix(ctx, prefix, opts)` |
| `Lease` | `LeaseGrant` | `LeaseService` | `Grant(ctx, ttl)` |
| `Lease` | `LeaseRevoke` | `LeaseService` | `Revoke(ctx, leaseID)` |
| `Lease` | `LeaseKeepAlive` | `LeaseService` | `KeepAlive(ctx, leaseID)` |
| `Lease` | `LeaseTimeToLive` | `LeaseService` | `TTL(ctx, leaseID)` |
| `Lease` | `LeaseLeases` | `LeaseService` | `List(ctx)` |
| `Cluster` | `MemberList` | `AdminService` | `Peers(ctx)` |
| `Maintenance` | `Status` | `AdminService` | `Status(ctx)` |
| `Maintenance` | `Defragment` | `MVCCKVService` | `Compact(ctx, revision)` |
| `Maintenance` | `Alarm` | -- | Returns current alarm state |
| `Auth` | `*` | -- | Stub (`Unimplemented`, mTLS instead) |

The `translate.go` module handles bidirectional type conversion between etcd protobuf types (`mvccpb.KeyValue`, `etcdserverpb.RangeRequest`, etc.) and QRDB service types (`service.KeyValue`, `service.RangeOptions`, etc.).

`translate.go` performs bidirectional type conversion between etcd protobuf
types (`mvccpb.KeyValue`, `etcdserverpb.RangeRequest`, …) and qrdb service
types (`service.KeyValue`, `service.RangeOptions`, …).

## Invariants

- **Every database is an independent etcd cluster.** Revision counters start at
  1 at database creation and are per database; watch events never cross
  databases; lease IDs are database-scoped; each database's data is encrypted
  with its own DEK through the `TenantBarrier`.
- **Keys are tenant-prefixed transparently.** The tenant comes from the client
  certificate OU; the prefix is added on write and stripped on read, so the
  client sees the key it asked for.
- **`Maintenance.Defragment` is mapped to `Compact`.** It is not a separate
  storage operation, and calling it discards revision history exactly as
  compaction does.
- **`Auth.*` returns `Unimplemented`.** This is permanent, not unfinished.
- **Authorization is per request**, mapping each etcd RPC to a qrdb permission
  (table above). An etcd client with no matching role gets a permission error on
  the RPC, not at connection time.

## Error semantics

| Condition | Behavior | Fails closed? |
|---|---|---|
| Built without `qrdb_etcd` | listener never starts | **yes** |
| `engine` is not `kv`, or `backend` is not `raft` | configuration rejected at startup | **yes** |
| TLS or auth disabled with etcd enabled | configuration rejected at startup | **yes** |
| Any `Auth.*` RPC | `Unimplemented` | **yes** — no fallback to password auth |
| Missing permission for the mapped operation | permission denied on the RPC | **yes** |
| Compacted revision requested | etcd's compacted-revision error | **yes** |
| Barrier sealed | requests refused | **yes** |

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
  operation in the table, per-database isolation (two databases, independent
  revisions and watches), and the `Auth` stub's `Unimplemented` response.
- **Integration**: real `etcdctl` and `clientv3` flows in the standalone stage.
  Multi-node etcd behaviour belongs to the cluster stage, which is **not
  currently wired into CI**.

## Observability

- `Maintenance.Status` reports through the qrdb admin service, so an etcd client
  sees the real node status rather than a synthesised one.
- `Maintenance.Alarm` returns the current alarm state.
- etcd operations are audited through [../audit/](../audit/) like any other
  data-plane access.
