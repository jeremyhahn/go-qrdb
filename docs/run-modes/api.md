# Run Modes — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

Run mode is not itself an API capability — it is the property that determines
which capabilities exist. Two things are observable over the wire: the reported
profile, and the shape of a refusal.

## Reporting the effective profile

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded | `scp` permission |
|---|---|---|---|---|---|---|
| Capabilities (incl. `mode`) | `GET /capabilities` | `Capabilities` | `admin.Capabilities` | `qrdb_capabilities` | `Client.Admin().Capabilities` | `admin:capabilities` |

The response is the **sealed** profile: build tags ∩ run mode ∩ config, plus a
`mode` field (`standalone` or `raft`).

A capability reported `false` is three-way ambiguous by design — not compiled,
mode-gated, or config-disabled — and `mode` plus the build is what
disambiguates. A caller that treats `false` as "not compiled" misdiagnoses a
mode gate.

## Capability families by mode

**Available in both modes, with the same API, the same semantics and the same
errors on every transport:** KV (get/put/delete/range/scan), batch, the
transaction / compare-and-swap family, **MVCC revisions and `at-revision`
reads, watch (prefix, revision resume, cancellation), lease (grant,
keep-alive, revoke, expiry), lock**, secondary indexes, collections and entity
CRUD, database management, SQL (`qrdb_sql`), time series and vector (their
tags), plus the always-compiled core — auth, RBAC, identity, seal/barrier,
credentials, audit, REST and gRPC transports.

**Raft mode only:** etcd compatibility, cluster membership and topology,
rebalance, shard backup/restore, and IsolationRaft tenancy. These are about a
cluster or a separate product surface, not about one database's data.

Every standalone backend (memory, file, pebble, objstore) composes the MVCC
engine, so the families above are served whatever the backend, and the
capability report says `mvcc`, `watch`, `lease` and `lock` true on every one.

MCP has no stream, so its SDK client watches by polling with a start revision
to stay gap-free. On a raft `sm_type` that keeps no revision history, the first
poll after an event needs replay, and the watch ends with the typed
`WatchStartRevisionUnsupportedError` rather than silently dropping changes
between polls.

## Refusal shape, per transport

A capability the node cannot serve — the etcd listener on a standalone node, or
a family its raft `sm_type` lacks — refuses with
`*service.CapabilityUnsupportedError`, whose message names the refused service
and, for a mode refusal, the remedy **`requires mode: raft`**.

A membership or snapshot call on a standalone node fails in `LocalBackend` with
`backend.ClusterOperationError` (`backend: cluster operation not supported:
<op>`). No transport's error classifier maps that type, so it does not take the
shape below.

| Transport | Refusal |
|---|---|
| REST | `501 Not Implemented` |
| gRPC | `UNIMPLEMENTED` |
| Unix (gRPC over UDS) | `UNIMPLEMENTED` |
| QUIC | `Unimplemented` |
| MCP | `UNIMPLEMENTED` (−32014), with the same message |
| Embedded | `*transport.CapabilityUnsupportedError` |

That type is the one every transport's classifier maps to "this node cannot do
that", and the SDK rebuilds `transport.CapabilityUnsupportedError` from each
transport's rendering of it, so a client gets the same typed answer whichever
transport it holds. The embedded transport translates
`service.MVCCBackendRequiredError` to the same type, and passes a lock's own
errors (`LockHeldError`, `NotLockHolderError`, `LockNotFoundError`) through
unchanged while translating the lease or MVCC refusal underneath a lock.

The etcd compatibility listener is refused at start on a standalone node
(`server.WithStandaloneMode`): `etcd: not implemented on this server: requires
mode: raft`.

## Conformance

`sdk/go/conformance`'s `TestCoordinationFamilies_SameOnStandaloneAndRaft` runs
ONE suite — lease lifecycle, lease expiry deleting its keys, prefix watch,
revision resume, cancellation, lock lifecycle — against a standalone memory
node, a standalone file node, a standalone pebble node and a raft node, on REST,
gRPC, Unix, QUIC, MCP and the embedded transport, plus a standalone objstore
node on the embedded transport.

## Versioning

`GET /capabilities` is an unversioned path. Nothing here is deprecated.
