# Audit — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-11

Compliance-grade audit logging: who did what, to which resource, with what
outcome.

## Problem

Compliance regimes (FedRAMP, PCI DSS, HIPAA) require a trail that survives the
subsystem that produced it and cannot be quietly gapped. Every subsystem writing
its own log format, to its own destination, produces something that cannot be
queried or correlated.

## Shape chosen

**One sink interface, several sinks, composed by a fan-out.**

```go
type AuditLogger interface {
	Emit(ctx context.Context, ev AuditEvent) error
	Query(ctx context.Context, f Filter) (*Page, error)
	Close() error
}
```

Every qrdb subsystem consumes that one abstraction. What happens to the event
is the implementation's problem — but **whether it was recorded is the
caller's**, which is what the `error` return says.

| Implementation | Destination | Emit reports |
|---|---|---|
| `ChainFileLogger` | append-only NDJSON with a SHA-256 hash chain, mode **0600**, daily/size rotation | `*WriteError` on marshal, write or fsync failure — **not** on rotation failure |
| `RingBuffer` | in memory, fixed capacity | always `nil` — the backing array cannot refuse a write, and overwriting the oldest event is the stated capacity policy, not a failure |
| `SlogSink` | the operational log stream via `log/slog` | always `nil` — `slog` reports nothing, and a sink that guessed would be worse than one that states its limit |
| `NoopLogger` | nowhere | always `nil` — discarding IS its durability boundary; an operator who configured no backend chose to keep no trail |
| `aggregateLogger` | fans out to all of the above | the **first** sink's error, after attempting **every** sink |

A second, narrower interface adapts per-decision authorization records onto
the same plane:

```go
type Logger interface {
	Log(ctx context.Context, entry *Entry) error
	Close() error
}
```

`decisionLogger` is its only implementation. It is a **pure adapter**:
whatever the sink says about the record, `Log` says to the authorization
middleware.

### The two properties that matter

**`Emit` returns `nil` only if the event reached the sink's durability
boundary.** For `ChainFileLogger` that means appended *and* fsynced (with
`syncPerWrite`, the default). This is what lets a caller for whom the record is
part of the operation — the authorization middleware — refuse rather than
proceed unrecorded. See *Fail-closed on audit write failure* below.

**The converse matters just as much: the error describes whether the RECORD IS
DURABLE, not whether every housekeeping step succeeded.** A rotation failure
reports nothing through `Emit`, because rotation runs *before* the append —
when it fails the record still lands and still fsyncs. Reporting it made the
middleware refuse requests whose audit entry had in fact persisted, and because
the size bound stayed exceeded it did so on *every* subsequent request. Rotation
failures ride `qrdb_audit_rotation_failures_total` and an `ERROR` log line
instead. See [engineering.md](engineering.md#the-emit-error-contract).

**The fan-out reaches every sink.** `aggregateLogger` attempts all of them even
after one fails, and returns the first error. A single backend failure must not
suppress the trail in the healthy ones.

### Alternatives rejected

- **Per-subsystem logging.** Rejected — one interface, one schema, one query
  surface.
- **An `Emit` with no error return.** Rejected. It was the original shape, and
  it decided the compliance question for every caller: the sink logged the
  failure at `Error` level and returned, the adapter returned `nil`
  unconditionally, and the middleware branch written to handle a failed audit
  write was unreachable by construction. A caller that wanted to fail closed
  could not.
- **A parallel `EmitErr` beside `Emit`.** Rejected. Two methods means two
  behaviours to keep in step across five implementations, and the old one
  remains the easy one to call. The signature change was made directly.
- **Failing fast in the fan-out.** Rejected: stopping at the first failed sink
  would let a broken file destination silently blank the ring buffer and the
  operational log stream that were both perfectly healthy.
- **Storing audit entries in the user keyspace.** Rejected — they live under
  `__sys:audit/`, inside the internal namespace, invisible to user enumeration.
- **A world-readable audit file.** Rejected: mode 0600.

## Fail-closed on audit write failure

**A gating decision is not made unless its audit record persisted.**

There are **two gates**, and they run on **one posture**:

| Gate | What it asserts | Component |
|---|---|---|
| `AuthMiddleware.authorize` | the RBAC/ABAC decision | `authorize` |
| `SOMiddleware` + `auth.SOAuthenticator.VerifySOPIN` | the security officer's second factor (PIN) | `so-dual-factor`, `so-auth` |

They are **two distinct assertions about the same request**, which is why
covering only the first was not enough: the sink can fail between them, and an
SO operation — rekey, custodian enrolment, CSR signing — is the most privileged
action the system offers. Before the SO gate was brought onto this posture an SO
PIN could verify, its record could fail to write, and the gated handler ran
anyway.

When either gate cannot record its event, it **refuses the request** —
`*audit.GateError` wrapping the sink's `*audit.WriteError`, which the
transports answer as HTTP 503 / gRPC `Unavailable`. `GateError.Component` says
which gate refused.

**One type, one posture, one lever.** The posture is `audit.FailurePolicy` and
it lives in `pkg/audit`, not in `pkg/auth/middleware` where it started.
`pkg/auth/middleware` imports `pkg/auth`, so a policy owned by the middleware
could never have been read by the SO factor — that import edge is precisely why
the SO gate ran on the weaker posture. A second configuration key would have
reintroduced the split.

The reasoning: an unrecorded **grant** is precisely the event audit exists to
prevent. A system that authorizes without recording has lost the property, and
doing so silently is worse than refusing. 503 rather than 403 because the
credential is valid and the grant may well be valid too — what failed is the
audit plane, and "retry" is the honest instruction.

The refusal is **uniform within a gate**: it does not depend on whether the
underlying outcome was a grant, a denial, a rate-limit refusal, a sealed-tenant
503, a missing SO PIN or a wrong one. "We answer only what we could record" is
one rule a reader can check; carving out the cases that would have refused
anyway makes it several rules that will drift, and buys nothing, since every
one of them refuses the caller either way. What does change is *what the caller
is told*: a wrong PIN on a healthy node is `401 SO PIN verification failed`,
and the same wrong PIN on a node that cannot write its trail is `503`, because
"retry" is the truth and "your PIN is wrong" would send a legitimate officer to
re-enrol.

**Gating records are not the same as post-hoc records, and the line is drawn at
"has it committed".** `SetSOPIN` and `RotateSOPIN` write their success record
*after* the commit has replicated through Raft; propagating a failed write
there would report a completed operation as failed, and the operator would
retry into `ErrSOPINAlreadySet`. Those stay on `EmitBestEffort` — deliberately,
and tested as such. The **verification**, which gates an operation that has not
yet happened, does not.

### What each transport answers

The refusal is a **retryable node-side fault**, and every transport has to say
so in whatever vocabulary it has. `middleware.ClassifyAuthError` answers the
transport-neutral question — *what kind of failure is this* — and each
transport maps the class onto its own wire, so the four cannot drift:

| Transport | Audit-write failure | Ordinary denial |
|---|---|---|
| REST | `503 Service Unavailable` | `403 Forbidden` |
| gRPC | `codes.Unavailable` | `codes.PermissionDenied` |
| QUIC | `wire.StatusUnavailable` | `wire.StatusPermissionDenied` |
| MCP | text: `authorization unavailable, retry: …` | text: `authorization denied: …` |

**Two named exclusions**, because a gap stated is better than a gap implied:

- **MCP carries no status code at all** — tool results are text. It cannot
  agree with the other three mechanically, so the distinction is carried in the
  wording. That is the whole channel it has.
- **The QUIC wire has no rate-limit status**, so a throttled caller rides
  `StatusUnavailable` rather than a dedicated code. That keeps the instruction
  the caller must act on — retry — and loses only the reason. Adding a status
  code is a wire-protocol change.

This matters most on the **tenant-barrier surface**, which is what an operator
and the SDK use to *recover* a node. Those handlers previously hard-coded
"permission denied" on every transport, so a node whose audit disk was full
reported its unseal endpoint as an authorization failure — terminal in every
client, and the precise misdiagnosis the 503 mapping exists to prevent.

### The operator's escape

Fail-closed has a real availability cliff: a full audit disk denies every
authorized request. `auth.audit_failure_policy: "fail-open"` is the documented
lever for an operator facing that, and it is **compliance-relevant** — see
[engineering.md](engineering.md#configuration) for the setting and
[user.md](user.md#when-the-audit-disk-fills) for the operational procedure.

It is **not a fallback.** Nothing degrades to it automatically, nothing retries
and then permits, and the node does exactly what the setting says for as long
as it says it. Selecting it logs a warning at every startup naming the property
given up.

### Auditing the audit failure

**A refusal caused by an audit outage cannot itself be audited.** The only sink
is the one that just failed; writing to it again would be a retry on a sink
that has already said no, and is exactly the fallback this feature forbids.

So the event is carried on two planes that do not depend on the audit plane:

- the counter `qrdb_auth_audit_write_failures_total{outcome="denied"|"permitted"}`
  for the authorization gate,
- the counter `qrdb_audit_gate_failures_total{component,outcome}` for every
  other gate — `component="so-auth"` and `component="so-dual-factor"` today,
- an `ERROR`-level line on the operational log stream.

The two counters are separate rather than one relabelled counter because the
predates the second and an operator's alert on it must keep meaning exactly
what it meant.

**The refusal is written to the sink exactly once.** Neither gate re-emits the
refusal it just produced: the only sink is the one that said no, and writing
again is the retry this feature forbids. It would also double-count the gap the
counter is supposed to measure exactly.

**During an audit outage the trail has a gap by construction.** What these
guarantee is that the gap is *visible and measurable* rather than silent — a
security control that fails quietly when its recorder is down is the worst
case.

The hash chain makes a back-filled record *detectable within a file*, which is
a weaker claim than it may read as, and it is worth stating the limit exactly:
**nothing in the daemon or the CLI verifies the chain.** `VerifyChain` exists on
the sink and has no non-test caller, so detection is available to an auditor
with their own tooling and is not a control the system runs. Verification also
does not currently span a rotation boundary — see *Known limits* below.

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
- **An authorization decision that could not be recorded is refused** by
  default. The audit record is part of the decision, not a side effect of it.
- **The same is true of the SO second factor.** The two gates share one posture
  value; there is no separate SO setting.
- **Querying the trail requires `audit:query`**, distinct from any operational
  permission.

## Correction notice

The *Event schema* section below and the factory table in
[engineering.md](engineering.md) describe a `kvLogger` / `fileLogger` /
`multiLogger` / `noopLogger` factory selected by an `audit.Config` with
`kv_backend` / `file_backend` keys. **No such factory, config struct or KV
backend exists in the code.** The canonical names are the ones in the table
above, composed at the node's wiring site rather than by a factory. The stale
description is older than the sink rework and is being corrected
document-by-document; it is recorded here rather than silently left in place.

## Known limits of chain verification

Both are pre-existing, neither is introduced by the fail-closed work, and both
bound what the *tamper-evidence* claim above is worth today.

- **Nothing verifies the chain.** `ChainFileLogger.VerifyChain` has no
  non-test caller anywhere — no CLI verb, no startup check, no periodic scan.
  The chain is written correctly; reading it back is an auditor's own job.
- **Verification does not span a rotation boundary.** A file's chain restarts
  at zero, but the first record appended after a rotation carries the *previous*
  file's digest in its `PrevHash`, while a whole-file verifier expects the first
  record of a file to carry none. A rotated file therefore fails verification at
  index 0 on entirely honest data. The `chain-anchor` entry written into the
  outgoing file records the handoff digest, so the cross-file link is
  recoverable by tooling that knows to look for it — but a naive whole-file
  check reports a break.

Until both are closed, treat `PrevHash` as **evidence an auditor can check**,
not as a control the system enforces.

## What this feature deliberately does not do

- **No log shipping.** There is no syslog, no remote sink, no agent. The file
  backend produces line-delimited JSON; shipping it is somebody else's job.
- **No retention policy.** Nothing prunes the trail.
- **No alerting.**
- **No redaction.** Callers must not put secret material in `Attributes` —
  values are never included by the subsystems that emit, and nothing strips them
  if a caller adds one.
