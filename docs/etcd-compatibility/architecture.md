# etcd Compatibility — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

> **Build-tag gated.** `qrdb_etcd` (`WITH_QRDB_ETCD=1`, on by default in the
> Makefile). `qrdb admin capabilities` reports `etcd`.

## Problem

A large amount of existing software — `etcdctl`, `clientv3`, and above all
`kube-apiserver` — speaks etcd v3 and nothing else. Asking those to adopt a new
protocol is not a realistic migration path.

## Shape chosen

**Per-database etcd clusters, not one global facade.** Each database with
`etcd: enabled` gets its own etcd-compatible gRPC server bound to that
database's shard-scoped service layer.

Each database with `etcd: enabled` gets its own etcd-compatible gRPC server bound to its shard's service layer (MVCC, Watch, Lease, Transaction). Unlike a global facade, each database is an independent "etcd cluster" with its own revision counter, watches, leases, and encryption barrier.

```
etcdctl/kube-apiserver --> etcd gRPC server --> qrdb service layer --> Raft
                           (per-database)       (shard-scoped)
```

The etcd gRPC server implements the core etcd v3 API surface (`etcdserverpb`) by translating etcd protobuf types to QRDB service calls. Each database operates as an isolated etcd cluster: revision counters, watch streams, lease tables, and encryption barriers are scoped to the database's shard.


The consequence is the design's whole point: each database is an **independent
etcd cluster** with its own revision counter, watch streams, lease table and
encryption barrier. Two databases on one node are two etcd clusters that cannot
see each other.

### Alternatives rejected

- **A single global etcd facade over the whole store.** Rejected: etcd's
  revision counter is global to a cluster, so one facade would force every
  database to share a revision space, and a compaction driven by one
  `kube-apiserver` would truncate history another consumer depended on.
- **Implementing etcd's `Auth` RPCs.** Rejected: `AuthServer` is a deliberate
  stub returning `Unimplemented`. This store authenticates with mTLS; adding a
  second, weaker username/password path to satisfy a protocol shape would be a
  security regression.
- **Reimplementing etcd's storage semantics.** Rejected: the MVCC engine, watch
  service, leases and transactions already provide them. The etcd server is a
  translation layer (`translate.go`), not a second implementation.

## Authentication

The etcd protocol uses mTLS exclusively -- no password authentication or etcd `Auth` RPCs. The certificate Common Name (CN) maps to the username, and the Organizational Unit (OU) maps to the tenant ID.

```
Client cert (CN=alice, OU=acme-corp)
  --> mTLS handshake
  --> Identity{Subject: "alice", TenantID: "acme-corp"}
  --> User store enrichment (roles)
  --> Database-scoped session (tenant-scoped)
```

The etcd `AuthServer` is implemented as a stub that returns `Unimplemented` for all auth RPCs. Clients must use mTLS client certificates instead of etcd's built-in username/password authentication.

## Authorization

Per-request RBAC authorization maps etcd API operations to QRDB permissions:

| etcd Operation | Permission |
|---|---|
| `KV.Range` | `kv:range` |
| `KV.Put` | `kv:put` |
| `KV.DeleteRange` | `kv:delete` |
| `KV.Txn` | `kv:transaction` |
| `KV.Compact` | `kv:compact` |
| `Watch.Watch` | `kv:watch` |
| `Lease.*` | `lease:*` |
| `Cluster.MemberList` | `admin:peers` |
| `Maintenance.Status` | `admin:status` |

## API Operations

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


## Multi-Tenancy

Tenant isolation is enforced at the storage level, identical to the SQL wire protocol. Each database with etcd enabled operates as a fully independent etcd cluster:

- **Independent revision counters** -- each database maintains its own monotonically increasing revision, starting from 1 at database creation.
- **Independent watch streams** -- watch events from one database never leak to another.
- **Independent lease tables** -- lease IDs are scoped to the database.
- **Independent encryption barriers** -- each database's data is encrypted with its own DEK via the TenantBarrier.

Keys are automatically prefixed with the tenant ID from the client certificate:

```bash
# Client cert: CN=alice, OU=acme-corp
etcdctl put /registry/pods/default/nginx '{"kind":"Pod",...}'
# Stored as: acme-corp//registry/pods/default/nginx

etcdctl get /registry/pods/default/nginx
# Reads: acme-corp//registry/pods/default/nginx
# Returns: key='/registry/pods/default/nginx' (prefix stripped)
```


## Package Structure

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


## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Per-database etcd gRPC server and its services | `pkg/api/etcd` | go-qrdb |
| Type translation, etcd protobuf ⇄ qrdb service types | `pkg/api/etcd/translate.go` | go-qrdb |
| MVCC, watch, lease, transaction semantics | `pkg/mvcc`, `pkg/watch`, `pkg/lease`, `pkg/service` | go-qrdb |
| Per-database barrier | `pkg/seal` — see [../barrier-seal/](../barrier-seal/) | go-qrdb |
| etcd protobuf definitions | — | **upstream etcd** (`etcdserverpb`, `mvccpb`) |

## What this feature deliberately does not do

- **No etcd `Auth` RPCs.** They return `Unimplemented`; use mTLS.
- **No global etcd cluster view.** `MemberList` and `Status` report the qrdb
  cluster, per database.
- **No etcd v2 API.**
- **No support outside `engine: kv` + `backend: raft`.** Configuration
  validation refuses other combinations, because etcd semantics need MVCC and
  replication.
