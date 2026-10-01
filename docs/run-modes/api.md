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

| Transport | Refusal (unary verbs: MVCC, lease, lock) | What the Go SDK client returns |
|---|---|---|
| REST | `501 Not Implemented` for MVCC; `404` for lease and lock, whose routes a standalone server does not register | an untyped `*transport.RequestError` |
| gRPC | `UNIMPLEMENTED` | `*transport.CapabilityUnsupportedError` |
| Unix (gRPC over UDS) | `UNIMPLEMENTED` | `*transport.CapabilityUnsupportedError` |
| QUIC | `Unimplemented` | `*transport.CapabilityUnsupportedError` |
| MCP | the raft-only tools are not registered: `tool not found` | an untyped `*transport.RequestError` |
| Embedded | — | `*transport.CapabilityUnsupportedError` |

Implemented by `pkg/server/standalone_gate.go` — nil singletons plus a gated
per-database service registry — so the refusal is structural rather than a
per-handler check that could be forgotten. The embedded transport is told it
serves a standalone composition (`embedded.WithStandaloneMode`, set by
`node.NewStandaloneNode`) and refuses the four raft-only families with the same
type the gRPC, Unix and QUIC clients reconstitute; it keeps
`*embedded.NilServiceError` for a service that should have been wired and was
not.

**Watch is the exception on the wire.** Opening a watch over gRPC, Unix, QUIC or
MCP does not fail on a standalone node; the refusal arrives on the stream. The
embedded transport refuses at open with `*transport.CapabilityUnsupportedError`.

**REST and MCP are a stated gap.** A consumer cannot match one type across all
six transports: REST answers lease and lock with a route-level `404` and MVCC
with an untyped `501`, and MCP does not register the tools. Both follow from
those servers' route and tool registration, so typing them is a change of its
own. `sdk/go/conformance/standalone_refusal_test.go` pins the per-transport
answer, so closing either gap flips its row there. Until then, check
`Capabilities()` up front rather than matching the refusal.

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
