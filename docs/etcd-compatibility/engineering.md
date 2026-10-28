# etcd Compatibility — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-26 at main 851fd06fc3
> Conflicts with this document are defects. Fix them here first.

> **Build-tag gated: `qrdb_etcd`** (`WITH_QRDB_ETCD=1`, on by default). Disable
> with `WITH_QRDB_ETCD=0`.

## Configuration

etcd compatibility is configured **per database**, not globally. `etcd` sits
directly on the database entry, the same as `mysql` and `postgresql`:
`config.DatabaseConfig` has no `protocol` field, so a nested `protocol.etcd`
block is an unknown key and the config load refuses the file.

```yaml
databases:
  - name: k8s-prod
    backend: raft
    etcd:
      enabled: true
      listen_addr: ":2379"
      advertise_client_urls:
        - "https://node1:2379"
```

**Validation is strict and fails startup, not the first request:**

- TLS and auth must both be enabled when the etcd protocol is active.
- The node must run in mode `raft`: the etcd listener is a raft-only surface
  (`capabilities.Etcd`), and a standalone node's listener refuses to start.
- Any `backend` serves it: the listener resolves the database through the
  service registry regardless of backend.
- `raft.sm_type` must be `mvcc`; any other state machine keeps no revision
  history and the listener refuses to start.

## RPC mapping

The RPC-to-service and RPC-to-permission map is
[api.md](api.md#rpc-coverage). `translate.go` converts qrdb `kv.KeyValue`
records to etcd `mvccpb.KeyValue` and builds the etcd response header.

## Exported surface

`pkg/api/etcd`:

- `Server`, built by `NewServer(dbName, addr string, opts ...ServerOption)`,
  with `Start`, `Stop` and `Addr`.
- `ServerOption` constructors: `WithTLS`, `WithClusterID`, `WithMemberID`,
  `WithRaftTerm`, `WithMVCC`, `WithTransaction`, `WithWatch`, `WithLease`,
  `WithAdmin` and `WithRequestAuthorizer`.
- `RequestAuthorizer` and `StreamGuard`, the seams the composer fills;
  `MethodPermission`, the RPC-to-permission map; `ValidateConfig`, the
  per-database config check.
- The typed errors in `errors.go`: `StartError`, `InvalidRequestError`,
  `AuthRequiredError`, `ScopeUnavailableError`, `MethodNotAllowedError`,
  `TenantMismatchError`, `PermissionDeniedError`, `AuthNotEnabledError` and
  `RevisionHistoryRequiredError`.

## Invariants

- **A listener serves exactly one database.** `startEtcd` resolves the
  database's MVCC, transaction, watch and lease services through the
  per-(tenant, database) service registry for the database's owning tenant; it
  is never handed the raw shard. A transaction, watch or lease service the
  database does not serve is left unregistered; any other resolution error
  refuses the start.
- **Every RPC is authenticated and authorized.** `etcd.RequestAuthorizer` runs
  as a unary and a stream interceptor: the node's authenticator chain, a
  refusal for a caller from another tenant (the system tenant included), the RPC's permission
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
- **A refused create is etcd's failed create.** Any other refusal -- an empty
  key, a permission or scope refusal from the watch service -- is `Created` and
  `Canceled` with watch ID `-1` and the reason, which the client returns as the
  watch's error; a watch that ends for a reason (an overflowed consumer, the
  database ceasing to serve) is cancelled with that reason rather than going
  silent.

## Error semantics

| Condition | Behavior | Fails closed? |
|---|---|---|
| Built without `qrdb_etcd` | enabling etcd is refused before anything is persisted: `service.CapabilityUnsupportedError` ("requires build tag qrdb_etcd") | **yes** |
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
| The state machine keeps no MVCC revision history (the default `pebble`) | the server refuses to start (`etcd.RevisionHistoryRequiredError`); a `Put` that reaches one is `FailedPrecondition`, never revision 0 | **yes** |

**Streams end when their database stops serving.** A `Watch` or
`LeaseKeepAlive` stream is guarded by its database (`etcd.StreamGuard`): the
moment the database is sealed or dropped, the stream ends with the sealed
refusal (`UNAVAILABLE`) or the dropped database's `NOT_FOUND`, even when the
client sends nothing more. The watch streams of the other transports (gRPC
and Unix `Watch`, QUIC, REST) end the same way, because every watch on a
catalogued database is opened through the service registry, which ties it to
the database. Authentication and the RPC's permission
are decided when a stream opens: a change to the caller's roles applies to its
next stream, not to one already open.

Typed errors live in `pkg/api/etcd/errors.go`; configuration validation in
`config.go`.

## Package structure

The file layout of `pkg/api/etcd` is in
[architecture.md](architecture.md#package-structure).

## Test strategy

```bash
make test-etcd            # etcd API tests
make coverage-etcd        # etcd coverage; fails below COVER_MIN (91%)
```

- **Unit**: per-RPC translation round-trips, authorization mapping for each
  operation in the table and default-deny for every other RPC, the refusal to
  start without authentication or a scope, and confinement: a listener resolved
  through the service registry returns only its database's keys from a store
  shared with other tenants and databases, refuses another tenant's caller and
  an unauthenticated one, and refuses the next RPC once the database is sealed.
- **Integration**: `clientv3` flows in `test/integration/etcd_test.go` against
  a self-spawned `qrdbd` (`make integration-test-etcd-compatibility`). The
  per-transport accounting is the `etcd-compatibility` row of
  [test/integration/COVERAGE.md](../../test/integration/COVERAGE.md).

## Observability

- `Maintenance.Status` reports the database's current revision (in the header
  and as `RaftIndex`). The other fields are fixed, not the node's real status:
  `Leader` is the listener's own member ID, `RaftTerm` is `1` and `DbSize` is
  `0`.
- `Maintenance.Alarm` returns no alarms.
- Log events, each carrying `database`: `etcd server started` (with `addr`),
  `etcd server stopped`, and `etcd server error` (with `error`) when serving
  ends in an error. The listener exports no metrics of its own.
- Each RPC's authorization decision is audited by the auth middleware, through
  [../audit/](../audit/) like any other data-plane access.
