# Audit — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

gRPC service `qrdb.v1.AuditService` — a dedicated service, separate from
`QRDBService`. The Unix socket transport serves the same gRPC wire over a UDS.

## Capability matrix

| Capability | REST | gRPC / Unix (`AuditService/`) | QUIC | MCP tool | Embedded (`Client.Audit()`) | `scp` permission |
|---|---|---|---|---|---|---|
| Query events | `POST /v1/audit/query` | `Query` | `audit.Query` | `qrdb_audit_query` | `AuditClient.Query` | `audit:query` |

**One capability, on all six transports.** Writing is not a capability: entries
are emitted by the subsystems that perform the audited operations, and there is
no endpoint to create, edit or delete one.

`Client.Audit()` returns **nil** when the transport does not implement
`AuditProvider`. Nil-check it.

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
| `tenant_id` | string | tenant |
| `limit` | int | default **100**, **maximum 1000** |
| `cursor` | string | pagination cursor from a previous response |

Outcomes carry a reason (`denied:missing permission`), so `outcome` matches
`success` exactly and matches `failure` / `denied` by prefix.

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

| Sink | Destination | Answers `Query`? |
|---|---|---|
| `ChainFileLogger` | `audit-events.ndjson`, hash-chained, one JSON object per line, mode **0600** | **yes** — scans the active file |
| `RingBuffer` | memory, fixed capacity (the node uses 10 000) | **yes** — serves recent events |
| `SlogSink` | the operator log via `log/slog` | no — reports `Total: -1` |
| `NoopLogger` | nowhere | no — reports `Total: -1` |
| `aggregateLogger` | every child | first **non-empty** page from any child |

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
| Barrier sealed | 503 | `UNAVAILABLE` |

## Versioning

Audit REST paths are under `/v1/`. Nothing here is deprecated.
