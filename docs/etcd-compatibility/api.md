# etcd Compatibility — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-01

This feature is **not** one of the six qrdb transports. It is a separate,
per-database gRPC listener speaking upstream etcd's own v3 API
(`etcdserverpb`), on its own port. A client that speaks etcd speaks to it
unchanged.

## Endpoint

| Property | Value |
|---|---|
| Protocol | etcd v3 gRPC (`etcdserverpb`) |
| Scope | **one listener per database** with `etcd: enabled` |
| Default listen address | `:2379` (`databases[].etcd.listen_addr`) |
| Advertised URLs | `databases[].etcd.advertise_client_urls` |
| Authentication | **mTLS only**, per RPC, through the node's authenticator chain; the caller must belong to the database's owning tenant; the system tenant is no exception (there is no cross-tenant database access) |
| Build tag | `qrdb_etcd` |

TLS and auth must both be enabled, the node must run in mode `raft`, and the
database `engine` must be `kv` -- any `backend` serves it -- or the listener
refuses to start. *(Corrected 2026-10-03: `backend: raft` was documented as
required here too; see "Requirements" below, which already carried the
correction.)*

## RPC coverage

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
| `KV` | `RangeStream` | — | **unimplemented**: `codes.Unimplemented` from the embedded `etcdserverpb.UnimplementedKVServer`, not a permission refusal |
| every other RPC — `Auth.*`, member changes, `Defragment`, `Snapshot`, `Hash`, `HashKV`, `MoveLeader`, `Downgrade` | | — | **refused** with `PERMISSION_DENIED` before any handler runs |

`KV.RangeStream` is a server-streaming RPC the `etcdserverpb.KV` service gained
in etcd v3.7 (a long-lived range read delivered incrementally rather than in
one response). `pkg/api/etcd`'s `kvServer` embeds
`etcdserverpb.UnimplementedKVServer` and does not override it, so a client
that calls it gets `codes.Unimplemented` — distinct from every RPC in the row
below, which this listener recognizes and actively refuses. A client wanting
a long-running range read uses `Range` with its existing pagination
(`RangeRequest.limit` and the returned `more` flag), as every client does
against this listener today.

The listener refuses to start without TLS and the node's authentication
(`etcd.AuthRequiredError`) or without a database catalog and service registry
to confine it (`etcd.ScopeUnavailableError`).

## Key namespacing

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

## Per-database isolation

Each etcd listener serves exactly one database, and a client can neither see nor
change another database's keys, watches or leases through it. Revisions are
those of the store serving the database — store-global on the shared shard, and
per group for a raft-isolated tenant — so they do not start at 1 per database
on the shared shard. *(Corrected: this section previously claimed a per-database
counter starting at 1.)*

## Kubernetes

Point `kube-apiserver` at QRDB's etcd endpoint to use QRDB as the Kubernetes backing store:

```bash
kube-apiserver \
  --etcd-servers=https://node1:2379,https://node2:2379,https://node3:2379 \
  --etcd-cafile=/etc/kubernetes/pki/etcd/ca.pem \
  --etcd-certfile=/etc/kubernetes/pki/etcd/client.pem \
  --etcd-keyfile=/etc/kubernetes/pki/etcd/client-key.pem
```

**Requirements:**

- The database must be `engine: kv` with `etcd: enabled`; enabling etcd on any other engine is refused at create and on update with `database.EtcdEngineUnsupportedError` (`400` / `INVALID_ARGUMENT`). Any backend serves it: the listener resolves the database's engines through the service registry.
- The node must run in mode `raft`: the etcd listener is a raft-only surface (`capabilities.Etcd`), and a standalone node's listener refuses to start (`service.CapabilityUnsupportedError`, "requires mode: raft"). *(Corrected: `backend: raft` was documented as required and was neither required nor checked -- the listener serves any backend through the service registry -- and `engine: kv` was documented as validated and was not.)*
- The `sm_type` must be `mvcc` (Kubernetes requires MVCC semantics: revisions, watches, leases, transactions).
- mTLS must be enabled with certificates signed by the same CA as the `kube-apiserver` client certificate.
- The RBAC role assigned to the Kubernetes client certificate must include `kv:range`, `kv:put`, `kv:delete`, `transaction:execute`, `watch:watch_prefix`, `admin:status` and `lease:*` permissions, and must be issued to a user of the tenant that owns the database. Add `mvcc:compact` if the client compacts (`kube-apiserver` does): it compacts that database's history only.

**Supported Kubernetes operations:**

- Pod, Service, ConfigMap, Secret, and all resource CRUD via etcd `KV.Put`/`KV.Range`/`KV.DeleteRange`
- Resource versioning via MVCC revisions (`KV.Txn` with `ModRevision` compares)
- Watch-based controllers via `Watch.Watch` with prefix matching
- Leader election via `Lease.LeaseGrant` + `KV.Txn` (compare-and-swap)
- Compaction via `KV.Compact` (triggered by `kube-apiserver` periodically)


## Errors

etcd's own error semantics are preserved: a compacted revision produces etcd's
compacted-revision error, a failed `Txn` returns `succeeded: false` rather than
an error, and an authorization failure surfaces on the RPC rather than at
connection time. Every `Auth` RPC, and every other RPC without a permission mapping, is refused with `PERMISSION_DENIED`.

## Versioning

etcd v3 only. There is no v2 API and none is planned.
