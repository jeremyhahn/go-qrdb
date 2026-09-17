# Run Modes — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-10

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

## Refusal shape, per transport

Every raft-only capability, called against a standalone server, refuses with a
typed error whose text names the remedy: **`requires mode: raft`**.

| Transport | Refusal |
|---|---|
| REST | `501 Not Implemented` |
| gRPC | `UNIMPLEMENTED` |
| Unix (gRPC over UDS) | `UNIMPLEMENTED` |
| QUIC | `Unimplemented` |
| MCP | `UNIMPLEMENTED` (−32014), with the same message |
| Embedded | typed Go error: `*service.CapabilityUnsupportedError` |

Implemented by `pkg/server/standalone_gate.go` — nil singletons plus a gated
per-database service registry — so the refusal is structural rather than a
per-handler check that could be forgotten.

The refusal is a **service-layer** error, `*service.CapabilityUnsupportedError`,
whose message names the refused service and the remedy
(`mvcc: not implemented on this server: requires mode: raft`). That type is the
one every transport's classifier already maps to "this node cannot do that",
and the SDK rebuilds `CapabilityUnsupportedError` from each transport's
rendering of it, so a client gets the same typed answer whichever transport it
holds. It used to be a REST transport type that only REST classified: REST
answered 501 while gRPC, unix, QUIC and MCP fell through to their unclassified
default and reported the mode gate as an internal fault — "the server broke",
retry — for a condition that is a deployment mode. The same type now also
carries "not configured on this node" refusals from the per-database service
registry, for the same reason.

## Capability families by mode

**Available in both modes:** KV (get/put/delete/range/scan), batch, **the
transaction / compare-and-swap family**, secondary indexes, collections and
entity CRUD, database management, SQL (`qrdb_sql`), time series and vector
(their tags; both need the MVCC-capable engine, so they are unavailable over
file/objstore backends), plus the always-compiled core — auth, RBAC, identity,
seal/barrier, credentials, audit, REST and gRPC transports.

**Raft mode only:** MVCC revisions and `at-revision` reads, `Compact`,
`RestoreToRevision`, watch (including replay), lease, lock, etcd compatibility,
cluster membership and topology, rebalance, shard backup/restore, and
IsolationRaft / IsolationVolume tenancy.

**Transactions are deliberately NOT gated** (owner ruling 2026-08-23), and this
list used to say they were — corrected here to match
`pkg/server/standalone_gate.go`, whose `raftOnlyGatedRegistry` passes
`TransactionService` straight through while refusing MVCC, watch and lease. A
standalone node is engine-direct and single-process, so a compare-and-swap
there is already linearizable: consensus is what makes a swap linearizable
across *replicas*, and a standalone node has none. Gating it at the wire while
the SDK's in-process transport served it would put the contradiction back one
seam over. The etcd-style `Txn` **is** refused, but as part of etcd
compatibility, which is listed separately above.

The SQL catalog keeps its internal MVCC and transaction plumbing in standalone
even though the MVCC *endpoints* are refused — the two are different surfaces.

## Versioning

`GET /capabilities` is an unversioned path. Nothing here is deprecated.
