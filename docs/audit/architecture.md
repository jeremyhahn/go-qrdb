# Audit — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Compliance-grade audit logging: who did what, to which resource, with what
outcome.

## Problem

Compliance regimes (FedRAMP, PCI DSS, HIPAA) require a trail that survives the
subsystem that produced it and cannot be quietly gapped. Every subsystem writing
its own log format, to its own destination, produces something that cannot be
queried or correlated.

## Shape chosen

**One `Logger` interface, several backends, chosen by a factory.**

```go
type Logger interface {
	Log(ctx context.Context, entry *Entry) error
	Close() error
}
```

Every qrdb subsystem consumes that one abstraction. What happens to the entry
is the implementation's problem, not the caller's.

| Implementation | Destination | Notes |
|---|---|---|
| `kvLogger` | the QRDB KVStore | keys `__sys:audit/{tenantID}/{timestamp_nano}` |
| `fileLogger` | a JSON file via `log/slog` | `O_CREATE\|O_APPEND\|O_WRONLY`, mode **0600**, mutex-serialized |
| `multiLogger` | fans out to several | **best-effort**: the first error is captured but every backend is still attempted |
| `noopLogger` | nowhere | when auditing is disabled or no backend is configured |

The nanosecond timestamp in the KV key gives **natural chronological ordering**
on a prefix scan — the audit log is queryable by range without a secondary
index.

`multiLogger`'s best-effort fan-out is the important property: **a single
backend failure must not suppress the trail in the healthy ones.**

### Alternatives rejected

- **Per-subsystem logging.** Rejected — one interface, one schema, one query
  surface.
- **Failing fast in `multiLogger`.** Rejected: stopping at the first failed
  backend would let a broken file destination silently blank the KV trail too.
- **Storing audit entries in the user keyspace.** Rejected — they live under
  `__sys:audit/`, inside the internal namespace, invisible to user enumeration.
- **A world-readable audit file.** Rejected: mode 0600.

## Event schema

The target compliance schema (`pkg/audit/types.go`) carries: `Timestamp`
(UTC), `Actor` (from the mTLS certificate CN), `TenantID`, `Role` at the time of
the event, `Resource`, `Action`, `Outcome` (`success`, `failure:<reason>`,
`denied:<reason>`), `Attributes`, `ClientIP`, `RequestID`, and **`PrevHash`** —
the hash of the previous entry, `sha256:<hex>`, forming a **tamper-detection
chain**.

Recording the actor's **role at event time** matters: a role changed afterwards
must not rewrite what the actor was allowed to do then.

> **Status caveat.** The source describes this as a phased implementation:
> the target schema is documented, with `Role`, extended `Outcome`,
> `Attributes`, `ClientIP`, `RequestID`, `PrevHash`, hash-chain computation and
> verification, and the query API landing in a later phase. Treat the full
> schema as the contract and verify field-by-field against `pkg/audit/types.go`
> before depending on any single field.

## Trust boundaries

- **Audit entries live under `__sys:audit/`**, inside the internal-key guard,
  so they are not enumerable through the user KV surface.
- **The file backend is mode 0600.**
- **`PrevHash` chains entries** so a deletion or edit is detectable rather than
  silent.
- **Permission-denied events are themselves audited** (`auth.denied`, result
  `denied`) — a failed attempt is exactly what a trail exists to record.
- **Querying the trail requires `audit:query`**, distinct from any operational
  permission.

## What this feature deliberately does not do

- **No log shipping.** There is no syslog, no remote sink, no agent. The file
  backend produces line-delimited JSON; shipping it is somebody else's job.
- **No retention policy.** Nothing prunes the trail.
- **No alerting.**
- **No redaction.** Callers must not put secret material in `Attributes` —
  values are never included by the subsystems that emit, and nothing strips them
  if a caller adds one.
