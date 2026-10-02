# Run Modes — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-01

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
disambiguates. This is recorded rather than smoothed over, because a caller
that treats `false` as "not compiled" will misdiagnose a mode gate.

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

*(Corrected: this section listed MVCC, watch, lease and lock as raft-only until
2026-10-01. A standalone node now composes them over the same engine, lessor
and watch hub as raft mode's MVCC state machine — see
[architecture.md](architecture.md#standalone--no-raft).)*

### The storage engine, not the mode, bounds the coordination families

A standalone node's backend selects its engine. Pebble and memory compose the
MVCC engine and serve every family above. **File and objstore compose the plain
KV engine**, which publishes changes but keeps no revisions and no leases:

| Family | pebble / memory | file / objstore |
|---|---|---|
| Watch (live) | served | served (except over MCP, below) |
| Watch with a start revision | served | refused: `WatchStartRevisionUnsupportedError` |
| MVCC revisions, `at-revision` | served | refused: `CapabilityUnsupportedError` |
| Lease | served | refused: `CapabilityUnsupportedError` |
| Lock | served | refused: `CapabilityUnsupportedError` (a lock is a lease) |
| Transactions | served | served on value and version compares; revision compares and lease-carrying ops refused: `CapabilityUnsupportedError` |

MCP has no stream, so its SDK client watches by polling with a start revision
to stay gap-free. On a store that keeps no revisions the first poll after an
event needs replay, and the watch ends with the typed
`WatchStartRevisionUnsupportedError` rather than silently dropping changes
between polls. This is the same answer a raft `sm_type` without revision history
gives.

The capability report is a **mode-level** statement, like `transaction`: it
reports `mvcc`, `watch`, `lease` and `lock` true on a standalone node whatever
its backend, and the engine boundary is answered per call.

## Refusal shape, per transport

A capability the node cannot serve — a raft-only family on a standalone node,
or a family the engine lacks — refuses with
`*service.CapabilityUnsupportedError`, whose message names the refused service
and, for a mode refusal, the remedy **`requires mode: raft`**.

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
node, a standalone pebble node and a raft node, on REST, gRPC, Unix, QUIC, MCP
and the embedded transport. `TestCoordinationFamilies_FileBackendServesWatchOnly`
pins the file-backend row of the table above on every transport.

## Versioning

`GET /capabilities` is an unversioned path. Nothing here is deprecated.
`embedded.WithStandaloneMode` was removed: it only selected the refusal this
document no longer describes.
