<!--
Copyright 2026 Jeremy Hahn

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# etcd Protocol Compatibility

QRDB supports etcd-compatible gRPC endpoints, allowing standard etcd clients (`etcdctl`, `clientv3`, `kube-apiserver`) to interact with QRDB databases as drop-in etcd replacements.

## Architecture

Each database with `etcd: enabled` gets its own etcd-compatible gRPC server bound to its shard's service layer (MVCC, Watch, Lease, Transaction). Unlike a global facade, each database is an independent "etcd cluster" with its own revision counter, watches, leases, and encryption barrier.

```
etcdctl/kube-apiserver --> etcd gRPC server --> qrdb service layer --> Raft
                           (per-database)       (shard-scoped)
```

The etcd gRPC server implements the core etcd v3 API surface (`etcdserverpb`) by translating etcd protobuf types to QRDB service calls. Each database operates as an isolated etcd cluster: revision counters, watch streams, lease tables, and encryption barriers are scoped to the database's shard.

## Build Tags

The etcd protocol is opt-in via a build tag (enabled by default in the Makefile):

| Flag | Build Tag | Description |
|------|-----------|-------------|
| `WITH_QRDB_ETCD=1` | `qrdb_etcd` | etcd v3 gRPC protocol |

Disable with `WITH_QRDB_ETCD=0`.

## Configuration

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

## CLI Usage

```bash
# Create a database with etcd protocol enabled
qrdb database create --name k8s --engine kv --backend raft --etcd --etcd-listen-addr :2379

# Enable etcd on an existing database
qrdb database update-etcd --name k8s --enabled --listen-addr :2379

# Disable etcd on a database
qrdb database update-etcd --name k8s --disabled
```

| Flag | Default | Description |
|------|---------|-------------|
| `--etcd` | `false` | Enable etcd protocol on database creation |
| `--etcd-listen-addr` | `:2379` | etcd gRPC listen address |
| `--etcd-advertise-client-urls` | (none) | Advertised client URLs for etcd endpoint discovery |

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

## Kubernetes Integration

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

## Client Examples

### etcdctl

```bash
# Set environment for mTLS
export ETCDCTL_API=3
export ETCDCTL_ENDPOINTS=https://localhost:2379
export ETCDCTL_CACERT=/etc/qrdb/tls/ca.pem
export ETCDCTL_CERT=/etc/qrdb/tls/client.pem
export ETCDCTL_KEY=/etc/qrdb/tls/client-key.pem

# Basic KV operations
etcdctl put mykey "hello world"
etcdctl get mykey
etcdctl del mykey

# Range queries
etcdctl get --prefix /config/
etcdctl get --prefix /config/ --keys-only

# Watch
etcdctl watch --prefix /events/

# Leases
etcdctl lease grant 60
etcdctl put mykey "value" --lease=694d7b0c6b5ae90a
etcdctl lease keep-alive 694d7b0c6b5ae90a
etcdctl lease revoke 694d7b0c6b5ae90a

# Transactions
etcdctl txn <<EOF
compares:
  mod("mykey") = "0"

success requests:
  put mykey "initial"

failure requests:
  get mykey
EOF

# Cluster info
etcdctl member list
etcdctl endpoint status
```

### Go clientv3

```go
import clientv3 "go.etcd.io/etcd/client/v3"

cli, _ := clientv3.New(clientv3.Config{
    Endpoints: []string{"https://localhost:2379"},
    TLS:       tlsConfig, // mTLS with client certificate
})
defer cli.Close()

cli.Put(ctx, "/config/db", "postgres://...")
resp, _ := cli.Get(ctx, "/config/", clientv3.WithPrefix())
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

## Makefile Targets

```bash
make test-etcd            # etcd API tests
make coverage-etcd        # etcd coverage (91%+ required)
```
