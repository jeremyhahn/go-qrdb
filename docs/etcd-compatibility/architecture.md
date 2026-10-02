# etcd Compatibility — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-01

> **Build-tag gated.** `qrdb_etcd` (`WITH_QRDB_ETCD=1`, on by default in the
> Makefile). `qrdb admin capabilities` reports `etcd`.

## Problem

A large amount of existing software — `etcdctl`, `clientv3`, and above all
`kube-apiserver` — speaks etcd v3 and nothing else. Asking those to adopt a new
protocol is not a realistic migration path.

## Shape chosen

**Per-database etcd listeners, not one global facade.** Each database with
`etcd: enabled` gets its own etcd-compatible gRPC server, confined to that
database: its MVCC, transaction, watch and lease services are resolved through
the node's per-(tenant, database) service registry for the database's owning
tenant, exactly as every other transport resolves a request.

```
etcdctl/kube-apiserver --> etcd gRPC server --> service registry --> qrdb service layer --> Raft
                           (per-database)       (tenant, database)
```

The etcd gRPC server implements the core etcd v3 API surface (`etcdserverpb`)
by translating etcd protobuf types to QRDB service calls. What a listener sees
is its database's keyspace, and nothing else:

- **Keys** are the database's `<tenant>/<database>/` namespace on the shared
  shard, or the tenant's own store under volume and raft isolation.
- **Watches** see only that keyspace.
- **Leases** are the ones this database granted; another database's lease ID
  is answered as a missing one (not-found; `LeaseRevoke` succeeds as a no-op).
- **Revisions** are those of the store serving the database: store-global on
  the shared shard, per group for a raft-isolated tenant's database.
- **Compaction** is the database's own. `Compact` discards this database's
  history below the revision and nothing else, on the shared shard as on a
  dedicated one: the store keeps a compaction floor per database namespace (see
  [../key-value/architecture.md](../key-value/architecture.md)).
- **Encryption at rest** is the store's: the node or tenant barrier, not a
  per-database one.

Two databases on one node are two listeners that cannot see each other's keys,
watches or leases; on the shared shard they do share one revision sequence.
*(Corrected: this section used to call each database an independent etcd
cluster with its own revision counter and encryption barrier; see
[api.md](api.md).)*

### Alternatives rejected

- **A single global etcd facade over the whole store.** Rejected: one facade
  would serve every tenant's and every database's keys to one etcd client, and
  a compaction driven by one `kube-apiserver` would truncate history every
  other consumer depended on. A listener per database confines keys, watches,
  leases and compaction to that database; on the shared shard its revisions
  stay store-global, and its compaction floor is its own.
- **Implementing etcd's `Auth` RPCs.** Rejected: the `Auth` service is not
  served — its RPCs are refused like every other unmapped RPC. This store
  authenticates with mTLS; adding a
  second, weaker username/password path to satisfy a protocol shape would be a
  security regression.
- **Reimplementing etcd's storage semantics.** Rejected: the MVCC engine, watch
  service, leases and transactions already provide them. The etcd server is a
  translation layer (`translate.go`), not a second implementation.

## Authentication

Every RPC is authenticated with the node's own authenticator chain — the mTLS
client certificate, mapped to an identity and enriched from the user store —
and authorized against the database's owning tenant:

- A caller from **another tenant** is refused (`PERMISSION_DENIED`, message
  `etcd: permission denied`); the system tenant, the store-global
  administrator, is admitted. The refusal names neither the database nor its
  owning tenant, so it does not tell a stranger what the port serves; the
  caller, its tenant, the owner and the database are logged on the server.
- The RPC's permission (table below) is checked for the owning tenant. For
  `Compact` that admits only the owning tenant's own identities:
  `mvcc:compact` is `ScopeSelfStrict`, so the system tenant cannot compact
  another tenant's database through its listener.
- A client that presents **no certificate** is refused (`UNAUTHENTICATED`).

The listener **refuses to start** (`etcd.AuthRequiredError`) unless the node has
TLS and its authentication configured, and refuses to start
(`etcd.ScopeUnavailableError`) when it cannot resolve the database through the
node's database catalog and service registry: the etcd `Auth` service is not
implemented, so either gap would serve the database to anyone who can reach the
port, or serve the raw shard.

```
Client cert (CN=alice, OU=acme-corp)
  --> mTLS handshake
  --> Identity{Subject: "alice", TenantID: "acme-corp"}
  --> User store enrichment (roles)
  --> owning tenant and RBAC check, database re-resolved
```

## Authorization

Per-request RBAC authorization maps each etcd RPC to a qrdb permission. The map
is default-deny: an RPC without a row is refused before its handler runs.

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

## API Operations

The served RPCs, the services behind them and their permissions are the table under Authorization above.

The `translate.go` module handles bidirectional type conversion between etcd protobuf types (`mvccpb.KeyValue`, `etcdserverpb.RangeRequest`, etc.) and QRDB service types (`service.KeyValue`, `service.RangeOptions`, etc.).


## Multi-Tenancy

The listener is **confined to its database**. Its MVCC, transaction, watch and
lease services are resolved through the same per-(tenant, database) service
registry every other transport uses, for the tenant that owns the database. On
the shared shard that is the `<owner-tenant>/<database>/` namespace: the prefix
is added on write and stripped on read, and a range can never leave it.

```bash
# etcd enabled on database "k8s" owned by tenant "acme-corp"
etcdctl put /registry/pods/default/nginx '{"kind":"Pod",...}'
# Stored as: acme-corp/k8s//registry/pods/default/nginx
etcdctl get "" --from-key
# Returns only keys of acme-corp/k8s, prefix stripped
```

A volume- or raft-isolated tenant's database is served from the tenant's own
store exactly as on every other transport. A database that is sealed, dropped
or whose tenant barrier is sealed is refused on the next request: the listener
re-resolves the database on every RPC.

Revision counters are those of the store serving the database: a database on
the shared shard sees the shard's store-global revisions, and a raft-isolated
tenant's database has its own group and its own counter. `Compact` compacts
the database's own history in either case: a `kube-apiserver` compacting its
database never discards another database's or another tenant's revisions.
*(Corrected: `Compact` was reserved to the system tenant on the shared shard,
because it then discarded every tenant's history; it is now per database.)* Watches and leases resolve through the
same scoped services, so events and lease-attached keys never cross databases.
*(Corrected: earlier revisions of this document claimed every database had its
own revision counter starting at 1, and that keys were prefixed with the
client's tenant; neither was what the code did.)*

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

- **No etcd `Auth` RPCs.** They are refused; use mTLS.
- **No global etcd cluster view.** `MemberList` and `Status` report the qrdb
  cluster, per database.
- **No etcd v2 API.**
- **No support outside `engine: kv`, or on a standalone node.** Enabling etcd
  on another engine is refused at create and on update
  (`database.EtcdEngineUnsupportedError`), and a standalone node's listener
  refuses to start, because the etcd listener is a raft-only surface. *(The
  reason given here was that MVCC, leases and watches are raft-only; a
  standalone node serves those since 2026-10-01, and the listener stays
  raft-only by its own capability flag.)* Any backend serves it. *(Corrected: `backend: raft` was documented as required and was neither required nor checked -- the listener serves any backend through the service registry -- and `engine: kv` was documented as validated and was not.)*
