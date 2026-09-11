# Audit — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Exported surface

`sdk/go`:

```go
func (c *Client) Audit() transport.AuditClient   // nil if the transport lacks AuditProvider
```

`pkg/audit`:

```go
// The sink interface. Emit reports whether the event was recorded.
type AuditLogger interface {
	Emit(ctx context.Context, ev AuditEvent) error
	Query(ctx context.Context, f Filter) (*Page, error)
	Close() error
}

func NewFileLogger(path string, opts ...ChainFileOption) (*ChainFileLogger, error)
func NewRingBuffer(capacity int) *RingBuffer
func NewSlogSink() *SlogSink
func NewNoopLogger() *NoopLogger
func NewAggregateLogger(loggers ...AuditLogger) AuditLogger

// The per-decision adapter the authorization middleware consumes.
type Logger interface {
	Log(ctx context.Context, entry *Entry) error
	Close() error
}

func NewDecisionLogger(sink AuditLogger) Logger
```

`sdk/go` re-exports the sink interface as `NodeAuditLogger`, the write error as
`NodeAuditWriteError`, and the posture below as `AuditFailurePolicy` /
`AuditFailClosed` / `AuditFailOpen` / `ParseAuditFailurePolicy` /
`AuditDecisionError`.

## Composition

There is **no factory and no `audit.Config`**. The node's wiring site composes
the sinks directly:

```go
chain, err := audit.NewFileLogger(filepath.Join(dataDir, "audit-events.ndjson"))
sink := audit.NewAggregateLogger(chain, audit.NewRingBuffer(10000), audit.NewSlogSink())
decisions := audit.NewDecisionLogger(sink)   // -> middleware.WithAuditLogger
```

**Auditing is always constructed**, regardless of `auth.enabled`: FedRAMP,
PCI DSS and HIPAA require a trail even where RBAC is off.

> **Correction.** Earlier revisions of this document described a
> `New(cfg, kvStore)` factory selecting between `kvLogger`, `fileLogger`,
> `multiLogger` and `noopLogger` on `audit.enabled` / `kv_backend` /
> `file_backend` / `file_path`. **None of those identifiers exists.** The
> canonical names are the ones above. The KV-backed sink and its
> `__sys:audit/{tenantID}/{timestamp_nano}` key layout, referenced throughout
> the rest of this document, are likewise not implemented — the durable sink is
> the NDJSON chain file. Recorded here rather than left in place; correcting
> the remaining references is tracked separately.

## The Emit error contract

**`Emit` returns `nil` if and only if the event reached that sink's durability
boundary and the sink is healthy.**

| Sink | Boundary | Returns |
|---|---|---|
| `ChainFileLogger` | appended, and fsynced when `syncPerWrite` (the default) | `*WriteError{Backend: <path>}` on marshal, write or fsync failure — **not** on rotation failure |
| `RingBuffer` | held in memory | always `nil` |
| `SlogSink` | handed to the `slog` handler | always `nil` |
| `NoopLogger` | deliberately discarded | always `nil` |
| `aggregateLogger` | every child attempted | the **first** child error, or `nil` |

`ChainFileLogger.Emit`'s failure ordering is deliberate:

1. A **marshal** failure returns immediately — no line exists, so there is
   nothing the append could salvage.
2. A **rotation** failure is not reported by `Emit` **at all**. Rotation runs
   before the append; when it fails the append still runs, still fsyncs, and
   the record is on disk exactly as it would have been. The record is durable,
   so `Emit` returns `nil` — reporting it would make the middleware refuse a
   request whose audit entry had in fact persisted. It is loud on `slog` and on
   `qrdb_audit_rotation_failures_total` instead.
3. **Write and fsync** errors are what `Emit` reports, because those are the
   ones that mean the record is not durable.

**The returned error describes whether the RECORD IS DURABLE, not whether every
housekeeping step succeeded.** That cuts both ways, and both directions have
been defects here: an fsync failure once skipped advancing the hash chain over
a record already in the file, and a rotation failure once refused a request
whose record had persisted. The second was the worse of the two, because it did
not stay one refusal — `rotate()` reopens unconditionally and re-stats the same
oversized file, so the size bound stays exceeded and **every** subsequent
`Emit` re-attempted the rotation. A directory that became unwritable while the
open handle stayed writable therefore took the node to 503-on-every-request,
including the tenant-barrier unseal an operator would use to recover, with a
healthy trail throughout.

A failed rotation is now re-attempted at most once per minute, so the fault
costs a bounded amount rather than a chain-anchor line and a full-file rescan
per event.

**Reporting a failure and recording what landed are separate obligations.**
The hash chain and the size always advance over bytes that reached the file,
*before* any error is returned. An fsync failure arrives after the append has
already put the record in the file; returning early without advancing
`prevDigest` left the next event linking to the pre-failure digest, so
`VerifyChain` reported `ChainBrokenError` on honest, untampered data. The
condition for advancing the chain is `n == len(line)` — a short write leaves a
torn line, which is not a record, and both `initPrevDigest` and
`verifyChainFile` skip a line that will not unmarshal.

Truncating back to the pre-write offset was the alternative and is worse: a
second I/O on a disk that just failed one, destroying a record that genuinely
landed, with a crash before it runs leaving exactly the inconsistency it was
meant to prevent. Advancing needs no crash handling, because `initPrevDigest`
re-derives by folding over **the records it can read back** rather than
trusting the last record's stored `PrevHash` — consistent whether the unsynced
record survived or not.

## Fail-closed on audit write failure

`decisionLogger.Log` propagates the sink's error verbatim. The authorization
middleware acts on it:

```go
// pkg/auth/middleware
type AuditFailurePolicy uint8

const (
	AuditFailClosed AuditFailurePolicy = iota  // the ZERO VALUE
	AuditFailOpen
)

func ParseAuditFailurePolicy(s string) (AuditFailurePolicy, error)
func WithAuditFailurePolicy(p AuditFailurePolicy) Option
```

- **`AuditFailClosed` is the zero value**, so an `AuthMiddleware` built by any
  constructor that never mentions the field is fail-closed. The same holds for
  `wiring.AuthMiddlewareConfig.AuditFailurePolicy` and for an omitted YAML key.
  A security default that has to be assigned is one that will eventually not be.
- Under `AuditFailClosed` the middleware returns `*middleware.AuditError`
  wrapping the sink's error, **uniformly**, whatever the underlying decision
  was. REST maps it to **503**, gRPC to **`codes.Unavailable`**.
- Under `AuditFailOpen` the decision stands and the failure is counted.
- With **no audit logger configured** the posture is inert: nothing could fail,
  so nothing is refused.

### Observability

```
qrdb_auth_audit_write_failures_total{outcome="denied"}      # fail-closed refused
qrdb_auth_audit_write_failures_total{outcome="permitted"}   # fail-open let through
qrdb_audit_write_failures_total{component="..."}            # a post-hoc record was lost
qrdb_audit_rotation_failures_total                         # the file stopped honouring maxBytes
```

The first two count authorization decisions. The third counts **every other**
audit record that failed to write, labelled by the subsystem whose trail now
has the gap — `user`, `role`, `custodian`, `ceremony`, `credentials`,
`so-auth`, `so-dual-factor`. Those call sites log and continue (see
*Callers other than the middleware*), and a log line is not something anyone
can alert on; the counter is.

`qrdb_audit_rotation_failures_total` is **deliberately separate** from the
write-failure counters. Those mean *a record was lost*; this one means *the
size bound is not being honoured and every record is still landing*. Folding
them together would make the one an operator pages on lie.

All are accompanied by an `ERROR` line on the operational log stream.

**Alert on all of them.** They are the only signals that survive the audit
plane being down.

**The refusal is not itself auditable** — the only sink is the one that failed,
and retrying it is forbidden. These planes are what make the resulting gap in
the trail visible rather than silent. A non-zero `outcome="permitted"` rate is
the exact size of the gap an operator accepted.

## Callers other than the middleware

`Emit` returning an error means every caller must state what it does with it.
There are exactly two honest answers:

| Answer | When | How |
|---|---|---|
| **Propagate** | the record is part of the decision being made | return it — the authorization middleware, and `sdk/go/transport/embedded.EmitAudit` |
| **Log and count** | the operation has **already committed** and this is its post-hoc record | `audit.EmitBestEffort(ctx, sink, component, ev)` |

```go
func EmitBestEffort(ctx context.Context, sink AuditLogger, component string, ev AuditEvent)
```

A user was created, a custodian enrolled, a credential rotated: there is no
decision left to refuse and no rollback the emitter could perform, so returning
the error would mislabel a completed operation as failed. `EmitBestEffort` is
the single home for that choice so it is made **visibly and greppably** rather
than by omission, and it counts
`qrdb_audit_write_failures_total{component=...}` so the resulting gap is
measurable at those sites too.

**It is not a way to avoid choosing.** A caller reaching for it where the
record *is* part of the decision has put the compliance property back where
this feature found it.

**One stated asymmetry.** The SO dual-factor middleware
(`middleware.SOMiddleware`) uses `EmitBestEffort`, including for the
PIN-verified success. It runs *behind* an already-fail-closed authorization
decision, and it is a free function that `auth.audit_failure_policy` does not
reach — two postures with one lever is worse than one posture stated plainly.
Extending the lever there is a separate change.

## Event schema

Target compliance schema (`pkg/audit/types.go`):

| Field | Meaning |
|---|---|
| `Timestamp` | UTC; auto-set to `time.Now()` if zero |
| `Actor` | identity from the mTLS certificate CN, JWT subject, or API key |
| `TenantID` | tenant owning the resource |
| `Role` | the actor's role **at the time of the event** |
| `Resource` | barrier, ceremony, keys, user, role, … |
| `Action` | initialize, unseal, sign, create, delete, … |
| `Outcome` | `success`, `failure:<reason>`, `denied:<reason>` |
| `Attributes` | JSON-serializable custom attributes |
| `ClientIP` | from `X-Forwarded-For` or the remote address |
| `RequestID` | for tracing across logs |
| `PrevHash` | `sha256:<hex>` of the previous entry — the tamper-detection chain |

> **Phased implementation.** The source documents the schema above as the
> target, with `Role`, extended `Outcome`, `Attributes`, `ClientIP`,
> `RequestID`, `PrevHash`, hash-chain computation/verification and the query API
> arriving in a later phase than the base `Entry`. **Verify individual fields
> against `pkg/audit/types.go` before depending on them.**

## Invariants

- **`Timestamp` zero means now.** An entry never carries a zero time.
- **KV keys are `__sys:audit/{tenantID}/{timestamp_nano}`.** The nanosecond
  component makes a prefix scan chronologically ordered, which is what makes
  range queries work without a secondary index.
- **The audit prefix is inside the internal-key guard** and is not enumerable
  through the user KV surface.
- **The file backend opens with `O_CREATE|O_APPEND|O_WRONLY`, mode `0600`**, and
  a mutex serializes concurrent writes.
- **`aggregateLogger` is a complete fan-out**: the first error is returned, but
  every remaining sink is still attempted, for both `Emit()` and `Close()`. A
  single sink failure must not suppress the trail in the healthy ones.
- **`Emit` returns `nil` only if the event was recorded.** The caller decides
  what that means for the operation it was performing.
- **An authorization decision that could not be recorded is refused** unless
  `auth.audit_failure_policy` says otherwise.
- **`kvLogger.Close()` is a no-op** — the KVStore lifecycle is managed
  externally.
- **Permission denials are audited** as `auth.denied` with the `ResultDenied`
  result value.
- **`PrevHash` chains entries**, so an edit or deletion is detectable.

## File output format

One JSON object per line:

```json
{"time":"2026-02-26T12:00:00Z","level":"INFO","msg":"audit",
 "tenant_id":"system","subject":"admin","action":"user.create",
 "resource":"user:operator","result":"success",
 "event_time":"2026-02-26T12:00:00Z"}
```

With metadata, as a nested group:

```json
{"...":"...","metadata":{"reason":"key compromise"}}
```

## Audited events

| Action | Resource format | Emitted by |
|---|---|---|
| `user.create` / `update` / `delete` / `enable` / `disable` | `user:{username}` | user management |
| `role.create` / `update` / `delete` / `assign` / `unassign` | `role:{name}` | role management |
| `cert.generate` / `cert.revoke` | `cert:{serial}` | certificate management |
| `seal.initialize` / `unseal` / `seal` / `rotate` / `rekey` | `barrier` | seal subsystem |
| `admin.drain` | `node:{id}` | admin operations |
| `admin.snapshot` | `shard:{id}` | admin operations |
| `admin.member.add` / `admin.member.remove` | `shard:{id}` | membership management |
| `auth.denied` | `{resource}` | authentication middleware |

## Configuration

There is no `audit:` block. The single audit-related setting is the failure
posture, and it lives with the authorization middleware that enforces it:

```yaml
auth:
  audit_failure_policy: "fail-closed"   # default; "fail-open" is the escape
```

| Value | Behaviour |
|---|---|
| `"fail-closed"`, or **omitted** | An authorization decision whose audit record cannot be written is **refused** (503 / `Unavailable`). |
| `"fail-open"` | The decision **proceeds**; the failure is counted on `qrdb_auth_audit_write_failures_total{outcome="permitted"}`. |
| anything else | **The boot fails** with `*middleware.InvalidAuditFailurePolicyError`. |

**This is a compliance setting, not a tuning knob.** `"fail-open"` gives up the
property that every authorization decision this node made is in the trail; the
trail acquires a gap whose size is exactly that counter. Auditors of a
FedRAMP / PCI DSS / HIPAA deployment should treat a node running `"fail-open"`
as a node whose authorization trail is not complete for the duration.

Three constraints on the setting, all enforced rather than documented:

- **It is not a fallback.** Nothing degrades to it automatically, nothing
  retries and then permits.
- **Selecting `"fail-open"` logs a `WARN` at every startup**, naming the
  property given up and the metric to watch. A posture that can be weakened
  silently will be.
- **An unrecognised value fails the boot.** Guessing `fail-closed` on a typo
  denies a fleet the operator meant to keep serving; guessing `fail-open`
  weakens the posture on a typo. Neither is a choice the parser may make.

Auditing itself has **no master switch** — the chain file sink is always
constructed, because the compliance regimes require a trail even where RBAC is
off.

## Known gap

The audit query handlers across REST, QUIC, MCP, Unix and the SDK transports
currently carry a **hardcoded permission string** pending a
`permission.AuditQuery` constant. The intended permission is `audit:query`; the
code does not yet use a constant for it. Tracked defect, not a naming choice —
the same gap affects the auth-factor handlers, see
[../auth-factors/api.md](../auth-factors/api.md).

## Persistence

KV entries live under `__sys:audit/{tenantID}/{timestamp_nano}` and are
Raft-replicated like any other state-machine record. **Nothing prunes them** —
there is no retention policy in this feature.

## Test strategy

- **Every sink's `Emit` error path has a test**, including the three that can
  never fail — each of those asserts the *reason* nil is the truthful answer,
  not merely the value.
- **The fan-out reaching every sink is the critical test** — a
  stop-at-first-error implementation would pass a naive "the aggregate returned
  an error" assertion and lose the trail in production, in the healthy sinks.
- **The fail-closed proof is end-to-end**: a real `ChainFileLogger` on a real
  file, faulted at `os.File.Write`, driven through a real HTTP request, is
  asserted to produce HTTP 503 carrying a `*middleware.AuditError` that unwraps
  to an `*audit.WriteError` naming the file — and the handler is asserted not
  to have run. A test that merely constructs those error types proves the
  constructors work and says nothing about whether any request produces one.
- **The default gets its own assertion** at three layers — the zero
  `AuditFailurePolicy`, the zero `wiring.AuthMiddlewareConfig` field, and the
  omitted YAML key — because the default *is* the security property.
- **The chain is verified ACROSS a fault, not just at it.** A test emits, fails
  the fsync, emits again healthy, and calls `VerifyChain`; another does the
  same across a close-and-reopen. A single-emit test that asserts only the
  returned error cannot see a desynchronised `prevDigest` and will pass over
  the bug.
- **Fsync and rotation failures use an unexported fault seam** on
  `ChainFileLogger` (`writeFunc` / `syncFunc` / `rotateFunc`, set only by tests
  in `pkg/audit`). They cannot be induced portably, and the `chmod` trick that
  fakes a rename failure is a no-op under a root CI runner.
- **Chronological ordering** on a prefix scan is tested, since it is what the
  query surface depends on.
- **Error tests** per function.

## Observability

The audit log **is** the observability surface for security-relevant events,
with one deliberate exception: the audit plane cannot observe its own failure,
so `qrdb_auth_audit_write_failures_total` and the `ERROR` line beside it carry
that one fact on planes that do not depend on it. Everything else is
[../api-server/](../api-server/)'s concern.
