# etcd Compatibility — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

This feature is **not** one of the six qrdb transports. It is a separate,
per-database gRPC listener speaking upstream etcd's own v3 API
(`etcdserverpb`), on its own port. A client that speaks etcd speaks to it
unchanged.

## Endpoint

| Property | Value |
|---|---|
| Protocol | etcd v3 gRPC (`etcdserverpb`) |
| Scope | **one listener per database** with `etcd: enabled` |
| Default listen address | `:2379` (`protocol.etcd.listen_addr`) |
| Advertised URLs | `protocol.etcd.advertise_client_urls` |
| Authentication | **mTLS only** — CN is the username, OU is the tenant |
| Build tag | `qrdb_etcd` |

TLS and auth must both be enabled, the database `engine` must be `kv` and its
`backend` must be `raft`, or the configuration is rejected at startup.

## RPC coverage

| etcd service | RPC | Backed by | Permission |
|---|---|---|---|
| `KV` | `Range` | `MVCCKVService.Range` | `kv:range` |
| `KV` | `Put` | `MVCCKVService.Put` | `kv:put` |
| `KV` | `DeleteRange` | `MVCCKVService.DeleteRange` | `kv:delete` |
| `KV` | `Txn` | `TransactionService.Execute` | `kv:transaction` |
| `KV` | `Compact` | `MVCCKVService.Compact` | `kv:compact` |
| `Watch` | `Watch` | `WatchService.Watch` / `WatchPrefix` | `kv:watch` |
| `Lease` | `LeaseGrant` | `LeaseService.Grant` | `lease:grant` |
| `Lease` | `LeaseRevoke` | `LeaseService.Revoke` | `lease:revoke` |
| `Lease` | `LeaseKeepAlive` | `LeaseService.KeepAlive` | `lease:keepalive` |
| `Lease` | `LeaseTimeToLive` | `LeaseService.TTL` | `lease:ttl` |
| `Lease` | `LeaseLeases` | `LeaseService.List` | `lease:list` |
| `Cluster` | `MemberList` | `AdminService.Peers` | `admin:peers` |
| `Maintenance` | `Status` | `AdminService.Status` | `admin:status` |
| `Maintenance` | `Defragment` | `MVCCKVService.Compact` | `kv:compact` |
| `Maintenance` | `Alarm` | — | returns the current alarm state |
| `Auth` | **all** | — | **`Unimplemented`** — mTLS instead |

`Maintenance.Defragment` maps to `Compact`: it is not a separate storage
operation, and calling it discards revision history exactly as compaction does.

`Auth` being unimplemented is permanent. There is no username/password path.

## Key namespacing

Keys are tenant-prefixed transparently, using the tenant from the client
certificate's OU. The prefix is added on write and stripped on read:


```bash
# Client cert: CN=alice, OU=acme-corp
etcdctl put /registry/pods/default/nginx '{"kind":"Pod",...}'
# Stored as: acme-corp//registry/pods/default/nginx

etcdctl get /registry/pods/default/nginx
# Reads: acme-corp//registry/pods/default/nginx
# Returns: key='/registry/pods/default/nginx' (prefix stripped)
```


## Per-database isolation

Each etcd-enabled database is a fully independent etcd cluster:

- **Independent revision counters** — each starts at 1 at database creation.
- **Independent watch streams** — events never cross databases.
- **Independent lease tables** — lease IDs are database-scoped.
- **Independent encryption barriers** — each database's data is encrypted with
  its own DEK through the `TenantBarrier`.

A client cannot see, or interfere with, another database's revisions, watches or
leases.

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

- The database must be created with `engine: kv`, `backend: raft`, and `etcd: enabled`.
- The `sm_type` must be `mvcc` (Kubernetes requires MVCC semantics: revisions, watches, leases, transactions).
- mTLS must be enabled with certificates signed by the same CA as the `kube-apiserver` client certificate.
- The RBAC role assigned to the Kubernetes client certificate must include `kv:range`, `kv:put`, `kv:delete`, `kv:transaction`, `kv:watch`, `kv:compact`, and `lease:*` permissions.

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
connection time. Every `Auth` RPC returns `Unimplemented`.

## Versioning

etcd v3 only. There is no v2 API and none is planned.
