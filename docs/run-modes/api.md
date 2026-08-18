# Run Modes — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

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
| MCP | tool error carrying the same message |
| Embedded | typed Go error |

Implemented by `pkg/server/standalone_gate.go` — nil singletons plus a gated
per-database service registry — so the refusal is structural rather than a
per-handler check that could be forgotten.

## Capability families by mode

**Available in both modes:** KV (get/put/delete/range/scan), batch, secondary
indexes, collections and entity CRUD, database management, SQL (`qrdb_sql`),
time series and vector (their tags; both need the MVCC-capable engine, so they
are unavailable over file/objstore backends), plus the always-compiled core —
auth, RBAC, identity, seal/barrier, credentials, audit, REST and gRPC
transports.

**Raft mode only:** MVCC revisions and `at-revision` reads, `Compact`,
`RestoreToRevision`, watch (including replay), lease, lock, transactions, etcd
compatibility, cluster membership and topology, rebalance, shard
backup/restore, and IsolationRaft / IsolationVolume tenancy.

The SQL catalog keeps its internal MVCC and transaction plumbing in standalone
even though the MVCC *endpoints* are refused — the two are different surfaces.

## Versioning

`GET /capabilities` is an unversioned path. Nothing here is deprecated.
