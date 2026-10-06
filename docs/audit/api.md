# Audit — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-05

gRPC service `qrdb.v1.AuditService` — a dedicated service, separate from
`QRDBService`. The Unix socket transport serves the same gRPC wire over a UDS.

## Capability matrix

| Capability | REST | gRPC / Unix (`AuditService/`) | QUIC | MCP tool | Embedded (`Client.Audit()`) | `scp` permission |
|---|---|---|---|---|---|---|
| Query events | `GET /v1/audit/events` | `Query` | `audit.Query` | `qrdb_audit_query` | `QueryAudit` | `audit:query` |
| **Verify chain** | `GET /v1/audit/verify` | `VerifyChain` | `audit.VerifyChain` | `qrdb_audit_verify` | `VerifyChain` | `audit:verify` |

**Two capabilities, on all six transports.** Writing is not a capability:
entries are emitted by the subsystems that perform the audited operations,
and there is no endpoint to create, edit or delete one.

Both capabilities answer from the **node-local** trail only. A separate,
cluster-wide replicated audit chain exists at the storage/Raft layer
([architecture.md](architecture.md#cluster-wide-replicated-trail)); nothing
proposes writes to it yet and it has no API surface on any transport.

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

The same refusal on QUIC is `StatusPermissionDenied`, on MCP the
permission-denied tool code, and on the embedded client
`*tenant.CrossTenantDeniedError`. Every transport returns each event with its
`tenant_id`, `client_ip`, `request_id` and `prev_hash`.

> **Known gap.** The audit query handlers on REST, QUIC, MCP, Unix and the SDK
> transports carry a **hardcoded permission string** pending a
> `permission.AuditQuery` constant. `audit:query` is the intended permission;
> the code does not yet reference a constant. Tracked defect — the same gap
> affects the auth-factor handlers, see
> [../auth-factors/api.md](../auth-factors/api.md).

## Query request

| Field | Type | Meaning |
|---|---|---|
| `from` / `to` | RFC3339 | time range |
| `actor` | string | actor identity |
| `action` | string | action |
| `resource` | string | resource |
| `outcome` | string | `success`, or a `failure` / `denied` **prefix** |
| `tenant_id` | string | the caller's own tenant, or omitted; any other tenant is refused |
| `limit` | int | default **100**, **maximum 1000** |
| `cursor` | string | pagination cursor from a previous response |

Outcomes carry a reason (`denied:missing permission`), so `outcome` matches
`success` exactly and matches `failure` / `denied` by prefix.

**Every query is confined to the caller's own tenant**, on all six transports.
An omitted `tenant_id` reads the caller's own events, never every tenant's; a
`tenant_id` naming another tenant is refused before anything is read. A
system-tenant caller reads the system tenant's events plus the platform events
that carry no tenant, and is refused every other tenant's. See
[architecture.md](architecture.md#who-may-read-which-events).

## Event schema

| Field | Meaning |
|---|---|
| `timestamp` | UTC; auto-set when zero |
| `actor` | from the mTLS certificate CN, JWT subject, or API key |
| `tenant_id` | tenant owning the resource |
| `role` | the actor's role **at the time of the event** |
| `resource` | barrier, ceremony, keys, user, role, … |
| `action` | initialize, unseal, sign, create, delete, … |
| `outcome` | `success` · `failure:<reason>` · `denied:<reason>` |
| `attributes` | JSON-serializable custom attributes |
| `client_ip` | from `X-Forwarded-For` or the remote address |
| `request_id` | for tracing across logs |
| `prev_hash` | `sha256:<hex>` of the previous entry — the tamper-detection chain |

**Recording the role at event time** is what stops a later role change
rewriting history.

> **Phased implementation.** The source describes `role`, the extended
> `outcome`, `attributes`, `client_ip`, `request_id`, `prev_hash`, the hash-chain
> computation/verification and the query API as arriving in a later phase than
> the base entry. Verify individual fields against `pkg/audit/types.go` before
> depending on them.

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

**The node's composition** is `AggregateLogger(ChainFileLogger, RingBuffer,
SlogSink)`, so `Query` is answered by the file and the ring.

> An earlier version of this page listed a KV backend at
> `__sys:audit/{tenantID}/{timestamp_nano}` and stated that **`Query` requires
> the KV backend**, unavailable by default. There has never been a KV backend
> or an `__sys:audit/` key space, and the conclusion was the opposite of the
> truth: querying works out of the box.

## Pagination

Cursor-based: pass `limit` (default 100, **hard maximum 1000**) and follow
`next_cursor` until it is empty. A query wider than 1000 events must be paged.

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

## Versioning

Audit REST paths are under `/v1/`. Nothing here is deprecated.
