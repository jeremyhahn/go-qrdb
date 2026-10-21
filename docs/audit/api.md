# Audit — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

gRPC service `qrdb.v1.AuditService` — a dedicated service, separate from
`QRDBService`. The Unix socket transport serves the same gRPC wire over a UDS.

## Capability matrix

| Capability | REST | gRPC / Unix (`AuditService/`) | QUIC | MCP tool | Embedded (`Client.Audit()`) | `scp` permission |
|---|---|---|---|---|---|---|
| Query events (node-local) | `GET /v1/audit/events` | `Query` | `audit.Query` | `qrdb_audit_query` | `QueryAudit` | `audit:query` |
| **Verify chain** (node-local) | `GET /v1/audit/verify` | `VerifyChain` | `audit.VerifyChain` | `qrdb_audit_verify` | `VerifyChain` | `audit:verify` |
| Query events (**replicated**) | `GET /v1/audit/events?replicated=true` | `Query{replicated:true}` | `audit.Query{"replicated":true}` | `qrdb_audit_query{replicated:true}` | `QueryAudit{Replicated:true}` | `audit:query` |
| **Verify chain** (**replicated**) | `GET /v1/audit/replicated/verify` | `VerifyReplicatedChain` | `audit.VerifyReplicatedChain` | `qrdb_audit_verify_replicated` | `VerifyReplicatedChain` | `audit:verify_replicated` |

**Two capabilities, on all six transports, each with a node-local and a
replicated form.** Writing is not a capability: entries are emitted by the
subsystems that perform the audited operations, and there is no endpoint to
create, edit or delete one.

The node-local pair answers from the **node-local** trail only.
The replicated pair answers from the cluster-wide replicated audit chain
([architecture.md](architecture.md#cluster-wide-replicated-trail)):
Raft-replicated rather than written by one process, readable identically
from every node in the group, and gated by the caller's tenant's
`AllowReplicatedAudit` entitlement — a tenant that is not entitled sees an
empty page (`Total: -1`) from the query form and a refusal
(`*service.ReplicatedAuditNotEntitledError`, mapped to 403 /
`PERMISSION_DENIED` on every transport) from the verify form, never a
request failure that looks like a server fault.

## The replicated chain read surface

**Query** (`replicated=true` / `Replicated: true` / `{"replicated":true}`)
reuses the SAME request and response shape as the node-local query on every
transport — one additional field, never a second message — and the SAME
tenant confinement (`tenant.ConfineAuditFilter`): an omitted tenant reads the
caller's own, another tenant is refused before either backend is reached.

**Verify** is a SEPARATE capability from the node-local `VerifyChain`,
because the result shape genuinely differs — see
[architecture.md](architecture.md#chain-hash-continuity-verification). It
takes one optional parameter, the tenant to verify (empty means the caller's
own; `tenant.Confine` refuses any other tenant), and answers:

| Field | Meaning |
|---|---|
| `verified` | `true` only if every surviving event folds into the chain's own authoritative head digest |
| `reason` | human-readable description of the break; empty when `verified` |
| `records` | surviving events folded |
| `head` / `low` | highest / lowest surviving sequence number |
| `genesis` | whether `low` is 1 (the chain's true origin) rather than a retention-pruned tail |

A non-2xx / non-OK response (REST 501, gRPC/Unix `Unimplemented`, QUIC
`StatusUnimplemented`, MCP `UNIMPLEMENTED`) means no replicated audit sink is
configured on this node at all. A 403 / `PERMISSION_DENIED` /
`StatusPermissionDenied` means the backend exists but the caller's tenant is
not entitled. A successful response with `"verified": false` is the TAMPER
VERDICT itself, exactly as for the node-local verify.

**`audit:verify_replicated` is a SEPARATE permission from `audit:verify`,
deliberately**, not a reuse: `audit:verify` is cross-scope (system-tenant
only), because the node-local hash chain it walks covers the whole node's
trail as one sequence, with no per-tenant slice to confine it to. The
replicated chain has the opposite shape -- every tenant's chain is its own
independent Raft-replicated table -- so `audit:verify_replicated` is
self-scoped: any tenant holding it may verify its OWN chain, and no other
tenant's.

**Verify chain** walks the active file and every rotation
archive as one sequence through `pkg/audit.Verify`, and reports the result —
it does NOT return a transport error for a broken chain. A non-2xx / non-OK
response (REST 501, gRPC/Unix `Unimplemented`, etc.) means the check could
not be PERFORMED (the configured `AuditLogger` implements no `Verifier` — see
the sink table below — the file is missing, or a rotation raced the walk); a
successful response with `"verified": false` is the TAMPER VERDICT itself.

The walk is bounded two ways. The caller's context governs it end to end:
cancellation or a deadline is checked per record and per rotation-set file,
not only once at the start, and a walk that does not finish in time answers
REST 504 / gRPC `DeadlineExceeded` (a cancelled call: REST 500 / gRPC
`Canceled`) rather than running to completion regardless. Every one of these
conditions is classified in the one table every transport renders
(`pkg/api/errclass`). And at most one walk runs at a time per logger: a
second call arriving while one is already in flight is refused immediately
with REST 503 / gRPC `Unavailable` rather than starting its own concurrent
full-trail scan. Neither case is a tamper verdict; retrying once the walk
can complete is the remedy.

See [user.md](user.md) for how to read a verification result and
[cli.md](cli.md) for `qrdb audit verify`.

| Field | Meaning |
|---|---|
| `verified` | `true` only if every linked file's chain checked out |
| `reason` | human-readable description of the break; empty when `verified` |
| `files` | rotation-set files that carry records and were linked |
| `records` | audit records verified, chain anchors excluded |
| `order` | linked files in chain order, head first |
| `genesis` | whether the walk reached the chain's origin (see "One clean result is not a complete one" in [user.md](user.md)) |
| `seed` / `handoff` | hex digest the head continues from (empty at genesis) / the tail ends on |

`Client.Audit()` returns **nil** when the transport does not implement
`AuditProvider`. Nil-check it.

Every transport returns each event with its `tenant_id`, `client_ip`,
`request_id` and `prev_hash`.

## Query request

| Field | Type | Meaning |
|---|---|---|
| `from` / `to` | RFC3339 | time range (gRPC: `from_rfc3339` / `to_rfc3339`) |
| `actor` | string | actor identity |
| `action` | string | action |
| `resource` | string | resource |
| `outcome` | string | `success`, or a `failure` / `denied` **prefix** |
| `tenant_id` | string | the caller's own tenant, or omitted; any other tenant is refused |
| `limit` | int | default **100**, **maximum 1000** |
| `cursor` | string | pagination cursor from a previous response |
| `replicated` | bool | read the cluster-wide replicated chain instead of the node-local trail; see [The replicated chain read surface](#the-replicated-chain-read-surface) |

Outcomes carry a reason (`denied:missing permission`), so `outcome` matches
`success` exactly and matches `failure` / `denied` by prefix.

**Every query is confined to the caller's own tenant**, on all six transports.
An omitted `tenant_id` reads the caller's own events, never every tenant's; a
`tenant_id` naming another tenant is refused before anything is read. A
system-tenant caller reads the system tenant's events plus the platform events
that carry no tenant, and is refused every other tenant's. The refusal is
403 / `PERMISSION_DENIED` on REST and gRPC/Unix, `StatusPermissionDenied` on
QUIC, the permission-denied tool code on MCP, and
`*tenant.CrossTenantDeniedError` on the embedded client. See
[architecture.md](architecture.md#who-may-read-which-events).

## Event schema

| Field | Meaning |
|---|---|
| `timestamp` | UTC; auto-set when zero |
| `actor` | the caller's Subject: the resolved account's immutable user id, or, for a caller resolved to no account, its authenticated name (certificate CN, JWT subject, API key) |
| `tenant_id` | tenant owning the resource |
| `role` | the actor's role **at the time of the event** |
| `resource` | barrier, ceremony, keys, user, role, … |
| `action` | initialize, unseal, sign, create, delete, … |
| `outcome` | `success` · `failure:<reason>` · `denied:<reason>` |
| `attributes` | JSON-serializable custom attributes |
| `client_ip` | from `X-Forwarded-For` or the remote address |
| `request_id` | for tracing across logs |
| `prev_hash` | hex SHA-256 digest of the previous entry, empty for the first — the tamper-detection chain; the replicated chain writes it as `sha256:<hex>` |

**Recording the role at event time** is what stops a later role change
rewriting history.

## Sinks

Every sink implements one interface, `audit.AuditLogger`. There is no backend
selector and no configuration: a caller composes the sinks it wants, and
`NewAggregateLogger` fans one `Emit` out to all of them.

| Sink | Destination | Answers `Query`? | Answers `Verify`? |
|---|---|---|---|
| `ChainFileLogger` | `audit-events.ndjson`, hash-chained, one JSON object per line, mode **0600** | **yes** — scans the active file | **yes** — the only sink with a chain to walk |
| `RingBuffer` | memory, fixed capacity (the node uses 10 000) | **yes** — serves recent events | no — holds no chain |
| `SlogSink` | the operator log via `log/slog` | no — reports `Total: -1` | no — holds no chain |
| `NoopLogger` | nowhere | no — reports `Total: -1` | no — holds no chain |
| `aggregateLogger` | every child | first **non-empty** page from any child | first child that implements `Verifier` (in production, `ChainFileLogger`, wired first) |

A sink answers `Verify` by implementing `audit.Verifier`
(`VerifyChainSet(ctx) (*ChainVerification, error)`); `pkg/audit.Verify` type-
asserts the configured `AuditLogger` against it and returns
`*VerificationUnsupportedError` when none of the composed sinks do.

`Total: -1` is how a sink that keeps nothing says **"not answerable here"**, as
distinct from `Total: 0`, which is a real empty result. An aggregate skips the
former and returns the first genuine page.

**The node's composition** is `audit.OpenTrailIn`, which returns
`NewAggregateLogger(ChainFileLogger, RingBuffer, SlogSink)`, so `Query` is
answered by the file and the ring.

## Pagination

Cursor-based: pass `limit` (default 100, **hard maximum 1000**) and follow
`next` until it is empty. A query wider than 1000 events must be paged.

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success | 200 | `OK` |
| `limit` above 1000 | clamped to 1000 | clamped |
| Malformed `from` / `to` | 400 | `INVALID_ARGUMENT` |
| No composed sink can answer | 200 with an empty result | `OK`, empty |
| Missing `audit:query` | 403 | `PERMISSION_DENIED` |
| `tenant_id` names a tenant other than the caller's — for every caller, the system tenant included | 403 | `PERMISSION_DENIED` |
| Barrier sealed | 503 | `UNAVAILABLE` |
| `replicated=true`, no replicated audit sink configured | 501 | `UNIMPLEMENTED` |
| `replicated=true`, tenant's `AllowReplicatedAudit` entitlement off (query) | 200, `total: -1`, empty events | `OK`, `total: -1`, empty events |
| `replicated=true`, tenant's `AllowReplicatedAudit` entitlement off (**verify**) | 403 | `PERMISSION_DENIED` |
| Replicated chain's cluster feature gate not yet active on the tenant's shard | 503 | `UNAVAILABLE` |
| Tenant names a dedicated-isolation (volume/raft) tenant not yet routable | 501 | `UNIMPLEMENTED` |

## Versioning

Audit REST paths are under `/v1/`. Nothing here is deprecated.
