# etcd Compatibility — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

> **Build-tag gated.** `qrdb_etcd` (`WITH_QRDB_ETCD=1`, on by default in the
> Makefile). `qrdb admin capabilities` reports `etcd`.

## Problem

A large amount of existing software — `etcdctl`, `clientv3`, and above all
`kube-apiserver` — speaks etcd v3 and nothing else. Asking those to adopt a new
protocol is not a realistic migration path.

## Shape chosen

**Per-database etcd listeners, not one global facade.** Each database with
`etcd.enabled: true` gets its own etcd-compatible gRPC server, confined to that
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
- **Encryption at rest** is the store's: the node or tenant barrier, or the
  database's own barrier where it has a dedicated store.

Two databases on one node are two listeners that cannot see each other's keys,
watches or leases; on the shared shard they do share one revision sequence.

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
  translation layer (`pkg/api/etcd`), not a second implementation.

## Authentication

Every RPC is authenticated with the node's own authenticator chain — the mTLS
client certificate, mapped to an identity and enriched from the user store —
and authorized against the database's owning tenant:

- A caller from **another tenant** is refused (`PERMISSION_DENIED`, message
  `etcd: permission denied`) -- the system tenant included: no tenant has
  access to another tenant's databases on any surface (see
  [../databases/architecture.md](../databases/architecture.md)). The refusal
  names neither the database nor its owning tenant, so it does not tell a
  stranger what the port serves; the caller, its tenant, the owner and the
  database are logged on the server.
- The RPC's permission (table below) is checked for the owning tenant. For
  `Compact` that admits only the owning tenant's own identities:
  `mvcc:compact` is `ScopeSelfStrict`.
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
  --> User store resolution: account "alice" (id 42), roles
  --> Identity{Subject: "42", UserID: 42, Username: "alice", ...}
  --> owning tenant and RBAC check, database re-resolved
```

## Authorization

Per-request RBAC authorization maps each etcd RPC to a qrdb permission. The map
is default-deny: an RPC without a row is refused before its handler runs. The
map, with the service behind each RPC, is [api.md](api.md#rpc-coverage)
(`methodPermissions` in `pkg/api/etcd/authorize.go`).

## Multi-Tenancy

What a listener sees is listed under "Shape chosen"; the key-namespacing
example is in [api.md](api.md#key-namespacing). A database that is sealed,
dropped or whose tenant barrier is sealed is refused on the next request: the
listener re-resolves the database on every RPC.

`Compact` compacts the database's own history, on the shared shard as on a
dedicated one: a `kube-apiserver` compacting its database never discards
another database's or another tenant's revisions. Watches and leases resolve
through the same scoped services, so events and lease-attached keys never cross
databases.

## Package Structure

```
pkg/api/etcd/
  server.go        Per-database etcd gRPC server lifecycle
  kv.go            KVServer (Range, Put, DeleteRange, Txn, Compact)
  watch.go         WatchServer with stream multiplexing
  lease.go         LeaseServer (Grant, Revoke, KeepAlive, TTL, Leases)
  auth.go          AuthServer, registered but never reached (Auth RPCs are refused)
  authorize.go     RPC-to-permission map; unary and stream authorize interceptors
  cluster.go       ClusterServer (MemberList)
  maintenance.go   MaintenanceServer (Status, Alarm; the rest are refused)
  translate.go     kv.KeyValue -> mvccpb.KeyValue; response headers
  config.go        Config validation
  errors.go        Typed errors
```

Every file above — production and test alike — carries `//go:build qrdb_etcd`
directly, the same treatment [sql/architecture.md](../sql/architecture.md)
gives `pkg/sql`: the whole etcd wire server is absent from a build without
the tag at its own declaration, not merely at `pkg/server/server_etcd.go`
(its one caller, also tagged).

## Build Tags

| `WITH_QRDB_ETCD=0` removes | Stays |
|---|---|
| `pkg/api/etcd` (this whole package) | The per-database `EtcdConfig` schema (`pkg/database`) — configuring etcd on a binary without the tag is refused with a typed `service.CapabilityUnsupportedError`, naming the missing build tag, not silently accepted |
| `go.etcd.io/*` (the etcd protobuf/client dependency) | `pkg/mvcc`, `pkg/watch`, `pkg/lease` — serve every transport, not etcd-specific |
| `qrdb database update-etcd`, and `database create`'s `--etcd*` flags | `qrdb database create`'s other flags; `qrdb admin capabilities` still reports `etcd: false` |

`go.etcd.io/*` is reached from nowhere else in this module: unlike the SQL
wire protocols, where go-mysql-server remains as PostgreSQL's engine even
with MySQL off, etcd has no shared core another family depends on, so
removing it removes the upstream dependency entirely. `test/buildgates`
(`etcd_dependency_test.go`) checks this with `go list -deps` over `cmd/qrdbd`
and `cmd/qrdb`: under the default tags both reach `pkg/api/etcd` and at least
one `go.etcd.io` package (the discovery floor); with `qrdb_etcd` off, neither
is reachable from either binary.

`test/performance/etcd-benchmark` pins its own `go.etcd.io/etcd/client/v3` in
a **separate** `go.mod`, against a released qrdb version, to benchmark this
feature from the outside — deliberately not part of this module's dependency
graph, and not affected by `WITH_QRDB_ETCD`.


## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Per-database etcd gRPC server and its services | `pkg/api/etcd` | go-qrdb |
| Type translation, etcd protobuf ⇄ qrdb service types | `pkg/api/etcd/translate.go` | go-qrdb |
| MVCC, watch, lease, transaction semantics | `pkg/mvcc`, `pkg/watch`, `pkg/lease`, `pkg/service` | go-qrdb |
| Encryption barriers (node, tenant, database) | `pkg/seal` — see [../barrier-seal/](../barrier-seal/) | go-qrdb |
| etcd protobuf definitions | — | **upstream etcd** (`etcdserverpb`, `mvccpb`) |

## What this feature deliberately does not do

- **No etcd `Auth` RPCs.** They are refused; use mTLS.
- **No global etcd cluster view.** `MemberList` and `Status` report the qrdb
  cluster, per database.
- **No etcd v2 API.**
- **No support on a standalone node.** Its listener refuses to start
  (`service.CapabilityUnsupportedError`): the etcd listener is a raft-only
  surface by its own capability flag, although a standalone node serves MVCC,
  leases and watches on its other transports. Any backend serves it.
