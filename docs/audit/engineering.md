# Audit — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

## Exported surface

`sdk/go`:

```go
func (c *Client) Audit() transport.AuditClient   // nil if the transport lacks AuditProvider

type AuditClient interface {
	EmitAudit(ctx context.Context, ev AuditEvent) error
	QueryAudit(ctx context.Context, f AuditFilter) (*AuditPage, error)   // f.Replicated reads the cluster-wide chain
	VerifyChain(ctx context.Context) (*AuditVerifyResult, error)          // node-local hash chain
	VerifyReplicatedChain(ctx context.Context, tenantID string) (*ChainContinuityResult, error)
}

// ChainContinuityResult is the replicated chain's verification outcome --
// a different shape from AuditVerifyResult: no files, order, seed or
// handoff, because one tenant has one Raft-replicated chain, not a
// rotation set.
type ChainContinuityResult struct {
	Verified bool
	Reason   string
	Records  int64
	Head     uint64
	Low      uint64
	Genesis  bool
}
```

`pkg/audit`:

```go
// The sink interface. Emit reports whether the event was recorded.
type AuditLogger interface {
	Emit(ctx context.Context, ev AuditEvent) error
	Query(ctx context.Context, f Filter) (*Page, error)
	Close() error
}

// Filter.Untenanted, with TenantID set, also matches events carrying no
// tenant (platform events). Never decoded from a request (json:"-").
type Filter struct{ /* ... */ TenantID string; Untenanted bool /* ... */ }

func NewFileLogger(path string, opts ...ChainFileOption) (*ChainFileLogger, error)
func NewFileLoggerIn(dir, name string, opts ...ChainFileOption) (*ChainFileLogger, error)
func OpenTrail(path string) (AuditLogger, error)
func OpenTrailIn(dir, name string) (AuditLogger, error)

var ErrOutsideTrailDir error // a trail name that leaves its directory
func NewRingBuffer(capacity int) *RingBuffer
func NewSlogSink() *SlogSink
func NewNoopLogger() *NoopLogger
func NewAggregateLogger(loggers ...AuditLogger) AuditLogger

// Implemented by backends capable of self-verifying their own tamper-evidence
// hash chain. *ChainFileLogger is the only first-party implementation.
type Verifier interface {
	VerifyChainSet(ctx context.Context) (*ChainVerification, error)
}

// The outcome of a Verify call. Verified is the only field a caller MUST
// check.
type VerificationReport struct {
	Verified bool
	Reason   string
	Files    int
	Records  int64 // unbounded over a trail's retention window
	Order    []string
	Genesis  bool
	Seed     string
	Handoff  string
}

// Verifies l's tamper-evidence hash chain and reports the outcome. Returns a
// non-nil error only when verification could not be PERFORMED at all
// (unsupported backend, missing file, a concurrent-rotation race, a canceled
// context, or a scan already in flight against the same logger) —
// Verified == false is a TAMPER VERDICT, carried in the report, not an error.
func Verify(ctx context.Context, l AuditLogger) (*VerificationReport, error)

// The per-decision adapter the authorization middleware consumes.
type Logger interface {
	Log(ctx context.Context, entry *Entry) error
	Close() error
}

func NewDecisionLogger(sink AuditLogger) Logger

// The GATING posture, and the emit that honours it.
type FailurePolicy uint8

const (
	FailClosed FailurePolicy = iota  // the ZERO VALUE
	FailOpen
)

func ParseFailurePolicy(s string) (FailurePolicy, error)

type InvalidFailurePolicyError struct{ Value string }
type GateError struct {
	Component string   // "authorize", "so-auth", "so-dual-factor"
	Err       error
}

func EmitGated(ctx context.Context, sink AuditLogger, component string, policy FailurePolicy, ev AuditEvent) error
func EmitBestEffort(ctx context.Context, sink AuditLogger, component string, ev AuditEvent)
```

`sdk/go` re-exports the sink interface as `NodeAuditLogger`, the write error as
`NodeAuditWriteError`, and the posture as `AuditFailurePolicy` /
`AuditFailClosed` / `AuditFailOpen` / `ParseAuditFailurePolicy` /
`AuditGateError` / `InvalidAuditFailurePolicyError`.

**The posture lives in `pkg/audit`, not in `pkg/auth/middleware`.**
`pkg/auth/middleware` imports `pkg/auth`, so the security-officer factor in
`pkg/auth` could not read a policy the middleware owned, and the SO gate
would run on a weaker posture than the authorization gate it sits behind.

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

**The daemon composes the trail before the seal subsystem**
(`openComplianceAudit` in `cmd/qrdbd`), once per process, and writes the
process-inspection opt-out to it as its first record. A trail that cannot be
opened is `*AuthBootstrapError` (step `compliance audit file`) and an opt-out
it cannot record is `*InspectionOptOutAuditError`; either stops start before
any barrier is built. The same sink is then handed to:

- `lifecycle.Infra.AuditLogger` for both `BuildSealServicesOnly` (the sealed
  window) and `BuildServices`. The ceremony service takes it through
  `ceremony.CeremonyService.SetAuditLogger`, and `ceremony.HolderDeps.Audit`
  carries it to every tenant and database holder authority;
- the user and role stores, the credential service, the authorization and SO
  gates, and every protocol server.

`sdk/go/node` passes the caller's `server.WithAuditLogger` sink as
`Infra.AuditLogger` for every graph it builds with a seal barrier -- the
sealed window's and the full one -- which are the graphs that carry a holder
authority; a node with seal disabled has no holders to record. With it goes
the node's `auth.audit_failure_policy` as `Infra.AuditFailurePolicy`.

The composition itself is `audit.OpenTrailIn(dataDir, name)` -- the chain
file, a ring of `audit.TrailRingSize` events and the operational log --
shared by the daemon, the desktop's embedded server and the SDK's flat
embedded server, at `audit.DefaultTrailFile` under the data directory.

**The trail is confined to the directory it is opened in.** The chain file
logger opens that directory as an `os.Root` (`NewFileLoggerIn`; `NewFileLogger`
and `OpenTrail` confine to the file's own directory) and resolves every file
operation of the trail inside it: the active file's open, a rotation's
rename, the archive name it picks and the directory sync after it,
retention's listing and removals, and the query and verification readers. A
symbolic link in the directory -- at the trail's name, or at a directory on
its path -- that resolves outside is refused (`*FileError` wrapping the
root's error), so the trail is never written, renamed or read anywhere else;
a link that stays inside is followed. A name that climbs out of the
directory is `*FileError` wrapping `ErrOutsideTrailDir` before anything is
opened. The confinement holds for the life of the logger: a directory on the
path swapped for an outward link after the trail opened fails the next
rotation rather than following it. The same sink reaches every barrier's own events through
`seal.NewComplianceAuditLogger`: the system barrier's configuration, the
tenant registry (`seal.WithAuditTrail`) and the database barrier manager
(`database.WithBarrierAuditTrail`). There is one trail and one emitter per
event.

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
housekeeping step succeeded.** A rotation failure must not refuse a request
whose record persisted: `rotate()` reopens and re-stats the same oversized
file, so the size bound stays exceeded and every later `Emit` would retry the
rotation, taking the node to 503 on every request, including the
tenant-barrier unseal an operator needs to recover. A failed rotation is
re-attempted at most once per minute (`rotateRetryInterval`), so the fault
costs a bounded amount rather than a chain-anchor line and a full-file rescan
per event.

**Reporting a failure and recording what landed are separate obligations.**
The hash chain and the size always advance over bytes that reached the file,
*before* any error is returned. An fsync failure arrives after the append has
already put the record in the file; returning without advancing `prevDigest`
would leave the next event linking to the pre-failure digest, and
`VerifyChain` would report `ChainBrokenError` on honest, untampered data. The
condition for advancing the chain is `n == len(line)` — a short write leaves a
torn line, which is not a record, and both `initPrevDigest` and
`verifyChainSegment` skip a line that will not unmarshal.

Truncating back to the pre-write offset is rejected: a
second I/O on a disk that just failed one, destroying a record that genuinely
landed, with a crash before it runs leaving exactly the inconsistency it was
meant to prevent. Advancing needs no crash handling, because `initPrevDigest`
re-derives by folding over **the records it can read back** rather than
trusting the last record's stored `PrevHash` — consistent whether the unsynced
record survived or not.

## The rotation boundary

**The chain spans a rotation; it does not restart per file.** `rotate()`
appends a `chain-anchor` record carrying the handoff digest to the outgoing
file, renames it, and opens a fresh one — and `Emit` fixed the outgoing
record's `PrevHash` and its successor digest *before* calling in, so the first
record of the new file carries that same handoff. Both sides of the boundary
state the same value.

| Function | Question it answers |
|---|---|
| `verifyChainSegment(ctx, dir, path)` | is this one file internally sound, and what does it hand to its successor? |
| `verifyChainSetAt(ctx, dir, path, generation)` | are every file in the rotation set sound **and** do they form one chain? |
| `ChainFileLogger.VerifyChain(ctx)` | `verifyChainSetAt` over the sink's own directory, verdict only |
| `ChainFileLogger.VerifyChainSet(ctx)` | the same, returning a `ChainVerification` |

`ctx` bounds the walk end to end (checked per record inside a segment, per
file inside a set) and `ChainFileLogger.VerifyChainSet` bounds concurrency to
one in-flight scan per logger -- see the error table below and
[api.md](api.md) for the transport-level status mapping.

Three details carry the design:

- **A segment is SELF-SEEDED.** It starts from the digest its own first record
  declares, which makes the check on that record vacuous and every other check
  exact. What the seed is worth is settled by `verifyChainSetAt`, which requires
  it to be some other file's handoff. Verifying against a caller-supplied seed
  instead would report one fault twice — as a break in the file *and* as a
  broken link.
- **The handoff is the digest standing BEFORE a terminal anchor**, or the final
  fold when the file has none (the active file). This mirrors `l.prevDigest`
  exactly: the anchor is written after the handoff is fixed and is never folded
  into it. An anchor that is *not* terminal — which a lost rename can leave
  mid-file — is folded like any other record, because the records after it
  chained over it.
- **Chain order comes from the digests, not the file names.** The walk finds
  the head (a file nothing hands off to), follows handoffs, and requires every
  record-bearing file to be reached. Files holding no records — an archive that
  is nothing but an anchor, the empty file a rotation has just opened — are
  verified and then left out of the linking, because they hand off nothing.

**A failed rename is re-fixed on the write path, and only there.** `rotate()`
appends the anchor *before* the rename, so when the rename fails — an
unwritable directory while the open handle stays writable, the scenario the
rotation backoff exists for — the anchor is durable in the very file the next
append extends, and `openFile` has re-folded that file including it. `Emit`
compares `prevDigest` against the value it marshalled with, and re-fixes the
record when they differ. A *successful* rotation deliberately does not take
that branch: it leaves `prevDigest` at the handoff, which is exactly what the
first record of the new file must carry. On the healthy path the branch is one
32-byte comparison that is always equal — no second marshal.

`initPrevDigest` seeds the same way, and it must: a file opened after a
rotation begins mid-chain, and folding it from zero would produce a digest no
record in the file agrees with, which the next event written after a restart
would link to, breaking the chain for real. It never reads the LAST record's stored `PrevHash`; only the
starting point comes from the file, and only from the one record whose
predecessor is not in the file to be folded.

Five error types out of the walk itself, because the remediation differs, plus
one from `pkg/audit.Verify`'s own type assertion:

| Type | Means |
|---|---|
| `*ChainBrokenError{Path, Index, Expected, Got}` | a **record** was changed — TAMPER VERDICT |
| `*ChainLinkError{Path, Other, Digest, Reason}` | a **file** was removed, duplicated or reordered — TAMPER VERDICT |
| `*ConcurrentRotationError{Path, Attempts}` | **no finding** — the sink rotated under the walk and no consistent view was obtained |
| `*VerificationCanceledError{Path, Err}` | **no finding** — the caller's context was canceled or its deadline exceeded before the walk finished; `Err` unwraps to `context.Canceled` / `context.DeadlineExceeded` |
| `*VerificationBusyError{}` | **no finding** — a scan against this logger was already in flight; `ChainFileLogger.VerifyChainSet` admits only one at a time |
| `*VerificationUnsupportedError{}` | **no finding** — `Verify`'s configured `AuditLogger` (or, for an aggregate, none of its children) implements `Verifier` |

The first two are TAMPER VERDICTS, folded into `VerificationReport.Verified =
false` by `Verify`, never returned as a Go error. The rest answer no question
about the trail's integrity and are returned as errors instead, so a
caller checking only `Verified` never mistakes "could not check" for "checked
and found broken."

**The walk does not hold the writer's lock**, and re-reads the directory
listing to find out whether it got a consistent view. A rotation renames the
active file and creates a new one, so a walk that straddles one lists the set
before and opens the files after: the new archive is missing from the list, the
replacement active file's seed has no predecessor in it, and the walk reports a
broken link **on honest data**. Taking `l.mu` across the walk would fix that and
is the wrong trade — it reads the entire trail, and every authorization
decision in the node would block behind it. So the listing is taken, the walk
runs, the listing is taken again; archive names are only ever added, so an
unchanged listing means the walk saw one generation. Up to five attempts with a
doubling backoff (`verifyScanAttempts`), then `*ConcurrentRotationError` — which says the trail moved,
**not** that anything is wrong with it. Reporting a tamper verdict from a race
is the one outcome worse than reporting nothing.

**A missing active file is a fault, not an empty set.** Unlinking a live audit
log would otherwise verify clean over the archives alone — or, before the first
rotation, over nothing at all — while the sink kept writing to an unreachable
inode. It is reported as a `*FileError` wrapping `fs.ErrNotExist`, but only
once *every* attempt has seen it absent across a walk in which nothing rotated:
`rotate()` unlinks the active name and re-creates it a moment later, and a
single listing cannot tell that from a deletion.

Two equal listings cannot tell it either, when the rotation is **mid-stride
across both**: a writer descheduled between its rename and its re-open, on a
loaded host, for longer than all five attempts together leaves every attempt
the same listing with the active name absent. So the logger's own verification
(`VerifyChain`, `VerifyChainSet`) also reads the writer's rotation sequence --
an atomic counter `rotate()` makes odd before the rename and even again when it
returns, by a deferred increment, so a failed rename, a failed reopen or a
recovered panic cannot leave it odd and mask every later finding -- before its
first listing and after its second.
An attempt counts only if both reads are the same even value; any other attempt
is a race, never an absence, and a walk that never gets a still view ends in
`*ConcurrentRotationError`. It takes no lock.

The one edit the walk cannot distinguish from healthy operation is the removal
of a contiguous **prefix** of the archives, because retention pruning does
exactly that. It is reported rather than refused: `ChainVerification.Genesis`
is false and `Seed` names the digest the surviving head claims to continue
from.

**The write path is untouched by this.** The rejected alternative — rotating
before the record's `PrevHash` is fixed, so each file is self-contained —
requires marshalling to get the line's length, rotating, recomputing the digest
and marshalling again, on the sink every authorization decision traverses.
`BenchmarkChainFileLogger_Emit_NoSync` and `BenchmarkChainFileLogger_Emit_Rotating`
measure the write path.

## Fail-closed on audit write failure

`decisionLogger.Log` propagates the sink's error verbatim. **Two gates** act on
it, on **one** `audit.FailurePolicy` value:

| Gate | Where | Component | How the policy reaches it |
|---|---|---|---|
| authorization decision | `AuthMiddleware.authorize` → `auditDecision` | `authorize` | `middleware.WithAuditFailurePolicy` |
| SO dual factor (route gate) | `middleware.SOGate` (REST `SOMiddleware`; gRPC, Unix, QUIC, MCP `WithSOGate`) | `so-dual-factor` | the `policy` **parameter**, set by `rest.WithAuditFailurePolicy` |
| SO PIN verification | `auth.SOAuthenticator.VerifySOPIN` | `so-auth` | `auth.WithSOAuditFailurePolicy` |

All three read the operator's single `auth.audit_failure_policy`, resolved once
per boot. **There is no second configuration key**: two keys would reintroduce
the split this design exists to remove.

- **`audit.FailClosed` is the zero value**, so an `AuthMiddleware`, an
  `SOAuthenticator`, a `rest.Server` or a `SOMiddleware` call built by any
  constructor that never mentions the field is fail-closed. The same holds for
  `wiring.AuthMiddlewareConfig.AuditFailurePolicy` and for an omitted YAML key.
  A security default that has to be assigned is one that will eventually not be.
  `SOMiddleware` takes the posture as a **positional parameter** rather than a
  variadic option so that "nobody passed one" and "somebody chose fail-closed"
  are not the same shape at the call site.
- Under `audit.FailClosed` the gate returns `*audit.GateError` wrapping the
  sink's error, **uniformly**, whatever the underlying outcome was. REST maps it
  to **503**, gRPC to **`codes.Unavailable`**.
- Under `audit.FailOpen` the outcome stands and the failure is counted.
- With **no audit logger configured** the posture is inert: nothing could fail,
  so nothing is refused.

### The SO path specifically

`SOMiddleware` distinguishes *a bad PIN* from *an unrecordable verification* by
the `AuthFailureAuditUnavailable` class, not by the wider
`AuthFailureUnavailable`. Guarding on
`AuthFailureUnavailable` would sweep in `*StoreUnavailableError` wrapping
`tenant.ErrTenantSealed`, which is the signal the sealed-window fallbacks exist
to act on. A wrong PIN on a healthy node is still `401 SO PIN verification
failed`; the same wrong PIN on a node that cannot write its trail is `503`.

**Why two classes, both mapped to 503.** `AuthFailureUnavailable` and
`AuthFailureAuditUnavailable` answer the wire identically -- 503 / gRPC
`Unavailable`, retryable, in every transport -- so this split changes no
response any caller sees. It exists for the CONTROL-FLOW code inside this
package, which reads one of these errors to decide what to do next rather than
what status to answer: the tenant-barrier bootstrap fallbacks
(`barrier_bootstrap.go`) and this SO-PIN gate must refuse outright on "could
not record the decision" (`AuthFailureAuditUnavailable`, from `*audit.GateError`),
while "the tenant's barrier is sealed" (`AuthFailureUnavailable`, from
`*StoreUnavailableError` wrapping `tenant.ErrTenantSealed`) is exactly the
signal those same fallbacks exist to admit. The type carries the distinction,
so a fallback written anywhere in this package inherits it by construction
rather than re-deriving a concrete-type check (`isAuditFailure`).

**`SetSOPIN` and `RotateSOPIN` keep `EmitBestEffort` for their own mutation
records, deliberately.** Their success record is written *after* `store.Put`
has replicated the commit, so propagating a failed write would report a
completed operation as failed and the operator would retry into
`ErrSOPINAlreadySet`. There is no rollback an emitter may perform. The
*rotation gate* is still fail-closed, because `RotateSOPIN` verifies the old
PIN through `VerifySOPIN` before committing anything.

### Observability

```
qrdb_auth_audit_write_failures_total{outcome="denied"}          # the authorization gate refused
qrdb_auth_audit_write_failures_total{outcome="permitted"}       # the authorization gate let it through
qrdb_audit_gate_failures_total{component,outcome}               # any OTHER gate: so-auth, so-dual-factor
qrdb_audit_write_failures_total{component="..."}                # a post-hoc record was lost
qrdb_audit_rotation_failures_total                              # the file stopped honouring maxBytes
```

The first two count authorization decisions. The third counts every **other**
gating record that failed to write, labelled by the gate and by what the policy
did — `component="so-auth"` and `component="so-dual-factor"` today. The fourth
counts **post-hoc** records that failed to write, labelled by the subsystem
whose trail now has the gap — `user`, `role`, `custodian`, `ceremony`,
`credentials`, `so-auth`. Those call sites log and continue (see *Callers other
than the middleware*), and a log line is not something anyone can alert on; the
counter is.

`qrdb_auth_audit_write_failures_total` and `qrdb_audit_gate_failures_total`
are deliberately separate rather than one relabelled counter: an operator's
alert on the authorization counter keeps meaning authorization decisions only.

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
| **Propagate** | the record **gates** an operation that has not yet happened | `audit.EmitGated(ctx, sink, component, policy, ev)` — the SO gates. The authorization middleware propagates through `auditDecision`, which keeps its own counter; `sdk/go/transport/embedded.EmitAudit` returns the sink's error verbatim, because there the emit IS the caller's operation |
| **Log and count** | the operation has **already committed** and this is its post-hoc record | `audit.EmitBestEffort(ctx, sink, component, ev)` |

```go
func EmitGated(ctx context.Context, sink AuditLogger, component string, policy FailurePolicy, ev AuditEvent) error
func EmitBestEffort(ctx context.Context, sink AuditLogger, component string, ev AuditEvent)
```

**The line is drawn at "has it committed", not at "is it a success".** Within
one gate every outcome is treated alike — success and refusal — because "we
answer only what we could record, except when we were going to refuse anyway"
is two rules that will drift and buys nothing: the caller is refused either
way. What changes is that it is told to *retry* rather than told its credential
was bad.

A user was created, a custodian enrolled, a credential rotated: there is no
decision left to refuse and no rollback the emitter could perform, so returning
the error would mislabel a completed operation as failed. `EmitBestEffort` is
the single home for that choice so it is made **visibly and greppably** rather
than by omission, and it counts
`qrdb_audit_write_failures_total{component=...}` so the resulting gap is
measurable at those sites too.

**It is not a way to avoid choosing.** A caller reaching for it where the
record *is* part of the decision has given up the compliance property.

**Both SO gates propagate.** The SO dual-factor middleware and
`auth.SOAuthenticator.VerifySOPIN` use `EmitGated` for every outcome, including
the PIN-verified success: the authorization decision and the SO factor are two
distinct assertions, written separately, and each must be recorded. Both read
the same `audit.FailurePolicy`, which is why the posture type lives in
`pkg/audit`.

## Event schema

`audit.AuditEvent` (`pkg/audit/audit_event.go`):

| Field | Meaning |
|---|---|
| `Timestamp` | UTC; auto-set to `time.Now()` if zero |
| `Actor` | the caller's `Identity.Subject`: the resolved account's immutable user id, or, for a caller resolved to no account, its authenticated name (certificate CN, JWT subject, API key) |
| `TenantID` | tenant owning the resource |
| `Role` | the actor's role **at the time of the event** |
| `Resource` | barrier, ceremony, keys, user, role, … |
| `Action` | initialize, unseal, sign, create, delete, … |
| `Outcome` | `success`, `failure:<reason>`, `denied:<reason>`, or `permitted` -- a gating record written before its effect, settled by a later `success` or `failure:<reason>` |
| `Attributes` | JSON-serializable custom attributes |
| `ClientIP` | from `X-Forwarded-For` or the remote address |
| `RequestID` | for tracing across logs |
| `PrevHash` | hex SHA-256 chain digest standing before this entry, empty for the first — the tamper-detection chain |

## Invariants

- **`Timestamp` zero means now.** An entry never carries a zero time.
- **The trail is not in the key-value store.** There is no audit key space and
  no audit state-machine record; durability is the append-only chain file, and
  recency is the in-memory ring.
- **`ChainFileLogger` opens with `O_CREATE|O_APPEND|O_RDWR`, mode `0600`**,
  and a mutex serializes concurrent writes.
- **A query reaches a sink already confined to the caller's tenant.** Every
  transport passes its filter through `tenant.ConfineAuditFilter` before
  `Query`: an empty tenant becomes the caller's, another tenant is a
  `*tenant.CrossTenantDeniedError`, and `Filter.Untenanted` (also match events
  with no tenant) is set for a system-tenant caller only. `Untenanted` is
  `json:"-"`, so the QUIC frame, which decodes its payload straight into a
  `Filter`, cannot carry it. The sinks match the tenant BEFORE paginating, so no
  other field — time range, cursor, limit — widens the result.
- **`Query` scans the ACTIVE file only.** Rotated archives are not searched, so
  a query window older than the current file returns nothing even though the
  events occurred. Ordering comes from append order, not from a key encoding.
- **A sink that keeps nothing answers `Query` with `Total: -1`**, never `0`.
  `-1` means "not answerable here" and `0` means "no matching events"; an
  aggregate skips the first and returns the first genuine page.
- **`aggregateLogger` is a complete fan-out**: the first error is returned, but
  every remaining sink is still attempted, for both `Emit()` and `Close()`. A
  single sink failure must not suppress the trail in the healthy ones.
- **`Emit` returns `nil` only if the event was recorded.** The caller decides
  what that means for the operation it was performing.
- **A GATING record that could not be written refuses its operation** unless
  `auth.audit_failure_policy` says otherwise. That covers the authorization
  decision, the SO dual-factor route gate and the SO PIN verification, on one
  policy value.
- **A refusal caused by an audit failure is never re-emitted to the sink.**
  `EmitGated` writes exactly once whatever the posture; a second write would be
  a retry on a sink that just said no, and would double-count the gap the
  counter is meant to measure exactly.
- **`NoopLogger.Close()` and `SlogSink.Close()` are no-ops** — neither owns a
  resource. `ChainFileLogger.Close()` closes the file it opened.
- **Permission denials are audited** as action `auth.authorize` with the
  `ResultDenied` result value.
- **`PrevHash` chains entries**, so an edit or deletion is detectable.

## File output format

One JSON-encoded `AuditEvent` per line:

```json
{"timestamp":"2026-02-26T12:00:00Z","actor":"1","tenant_id":"system",
 "resource":"user:2","action":"create","outcome":"success",
 "attributes":{"username":"operator"},"prev_hash":"<hex>"}
```

A decision record from the authorization middleware maps `audit.Entry`
through `NewDecisionLogger`: `Subject` becomes `actor`, `Result` becomes
`outcome`, and `Metadata` becomes `attributes`.

## Audited events

| Action | Resource format | Emitted by |
|---|---|---|
| `create` / `update` / `delete` / `enable` / `disable` / `add_cert_binding` / `remove_cert_binding` | `user:{id}` | user management (`pkg/user`) |
| `create` / `update` / `delete` / `reconcile` | `role:{id}` | role management (`pkg/role`) |
| `cert.issue` | `cert:{serial}` | every signature by a tenant's login CA (`CertEnrollmentService.SignCertificate`): CSR issuance for an existing account |
| `cert.enroll` | `user:{id}` | a bring-your-own certificate bound to an existing account (`CertEnrollmentService.EnrollCertificate`) |
| `cert.revoke` | `cert:{serial}` | every revocation (`DefaultCertProvider.RevokeCert`), deny-by-serial included |
| `initialize` / `unseal` / `seal` / `quorum-complete` / `distribute` / `change_password` / `enable_encryption` / `import-sealed-blob` / `holder-record-conflict` | `barrier` | every barrier -- system, tenant, database -- through `seal.NewComplianceAuditLogger`; see [../barrier-seal/engineering.md](../barrier-seal/engineering.md#observability) |
| `auth.authorize` | `{permission}` | the authorization middleware, every decision (`success` or `denied`) |
| `holder-register` / `holder-claim` / `holder-quorum-submit` / `holder-rekey-start` / `holder-revoked` / `holder-unenrol` | `share-holder` | the share-holder engine (`pkg/ceremony`), every scope |

The share-holder events carry the scope's owning tenant as `tenant_id` and
these attributes only: `scope` (`system`, `recovery`, `tenant:<id>`,
`database:<tenant>/<name>`), `domain`, `holder`, and per event `role`
(register), `share_index` (claim), `op` (quorum submit, rekey start),
`threshold` and `policy` (rekey start). The actor is the authenticated
identity; with none, the holder once it has proved itself (a claim, a quorum
submission), the enroller, or `share-holder-quorum` for a revocation the
quorum completed; otherwise `anonymous`. A holder name that is not a valid
principal name (1-64 characters of `A-Z a-z 0-9 . _ @ -`) is recorded as
`(invalid-name)`: on a refusal it comes from an unauthenticated caller.

A refusal's outcome is `denied:<reason>` for a claim or submission that did
not authenticate and `failure:<reason>` otherwise, where `<reason>` is a
**fixed token** for the error's class -- `not-found`, `challenge-mismatch`,
`expired`, `verification-failed`, `limit-reached`, `duplicate`,
`self-enrolment`, `sealed`, `invalid-request`, `factor-not-admitted`,
`ca-unavailable`, `round-in-progress`, `breaks-quorum`, `stale-roster`,
`roster-missing`, `initialized`, `initialization-in-progress`,
`audit-unavailable`, or `unknown`. An error's own text is
never recorded: a factor's refusal (a go-xkms FIDO2, PIV, PKCS#11 or TPM
factor's among them), an HPKE or a storage error is free text the trail could
not redact, as the SO gate's fixed reasons (`not-set`, `locked`, `invalid`)
already avoid.

**Which records gate, and which are post-hoc.** A registration, a claim, a
rekey opening and an unenrolment are recorded **before** they take effect -- a registration
once validated and before the CA issues the certificate, a claim once the
holder has proved its key and before the envelope leaves the store, a rekey
once the barrier has validated it and before its round opens
(`seal.RekeyParams.Admit`) -- with `audit.EmitGated` under the node's one
posture, `HolderDeps.AuditPolicy` (`auth.audit_failure_policy`, the same
value the authorization and SO gates follow). Under `fail-closed`, the
default, a record the trail refuses refuses the operation with
`*audit.GateError` (`Unavailable` on every transport) and nothing changes:
no certificate is issued, the envelope stays held and claimable, no round
opens. Under `fail-open` the operation proceeds and the gap is counted on
`qrdb_audit_gate_failures_total{component="share-holder"}`.

**A gating record says `permitted`, never `success`.** It is written before
the effect, so it cannot know whether the effect will happen, and the trail is
hash-chained: a `success` written there would stay on it whatever followed.
Each gated operation therefore leaves its records in one of three sequences:

| Sequence | Meaning |
|---|---|
| `permitted`, then `success` | admitted, and the effect completed -- the holder stored, the envelope delivered, the round opened, the unenrolled holder removed |
| `permitted`, then `failure:<reason>` | admitted, and the effect then failed -- the CA, a factor's enrolment, the store |
| `failure:<reason>` or `denied:<reason>` alone | refused before the gate, so nothing was permitted -- invalid input, a duplicate, an unproven claimant, a rekey the barrier refused |

The second record is post-hoc (`audit.EmitBestEffort`): the operation has run
when it is written. A gate refusal under `fail-closed` leaves neither record
-- the sink that refused the first would refuse the second, and the
operation did not run. Input validation runs before the gate -- a registration's
factor names (an empty, repeated or unknown one) and its designated factor
included -- so invalid input is never `permitted`; a factor that refuses the
enrolment itself does so after the gate, `failure:invalid-request`.
Refusals, quorum submissions and revocations stay post-hoc
(`audit.EmitBestEffort`, `qrdb_audit_write_failures_total{component="share-holder"}`):
the operation has already been refused or completed when they are written.

## Configuration

There is no `audit:` block. The single audit-related setting is the failure
posture, and **one value governs every gate**:

```yaml
auth:
  audit_failure_policy: "fail-closed"   # default; "fail-open" is the escape
```

| Value | Behaviour |
|---|---|
| `"fail-closed"`, or **omitted** | A gating record that cannot be written **refuses** its operation (503 / `Unavailable`) — the authorization decision and the SO PIN verification alike. |
| `"fail-open"` | The operation **proceeds**; the failure is counted on `qrdb_auth_audit_write_failures_total{outcome="permitted"}` or `qrdb_audit_gate_failures_total{component,outcome="permitted"}`. |
| anything else | **The boot fails** with `*audit.InvalidFailurePolicyError`. |

**This is a compliance setting, not a tuning knob.** `"fail-open"` gives up the
property that every authorization decision *and every SO second factor* this
node made is in the trail; the trail acquires a gap whose size is exactly those
counters. Auditors of a FedRAMP / PCI DSS / HIPAA deployment should treat a node
running `"fail-open"` as a node whose authorization trail is not complete for
the duration.

**There is deliberately no second key.** An `auth.so_audit_failure_policy` would
let a deployment record one of the two assertions and not the other, which is
the split this design exists to remove. The daemon resolves the string once at
boot and hands the resulting `audit.FailurePolicy` to
`middleware.WithAuditFailurePolicy`, `rest.WithAuditFailurePolicy` and
`auth.WithSOAuditFailurePolicy`.

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

`pkg/tenant` holds the confinement every transport applies:

```go
func CallerTenant(ctx context.Context) string                        // identity's tenant, else the bound one, normalized
func Confine(ctx context.Context, requested, op string) (string, error) // "" or own -> own; other -> *CrossTenantDeniedError
func ConfineAuditFilter(ctx context.Context, f audit.Filter) (audit.Filter, error)
```

**The SDK speaks the server's wire shape.** The audit query's wire types are
`audit.Filter` and `audit.Page`, whose JSON names (`tenant_id`, `client_ip`,
`request_id`, `prev_hash`) differ from `transport.AuditFilter` and
`transport.AuditEvent` (`tenantID`, `clientIP`, ...). The REST, QUIC and MCP SDK
clients convert through `sdk/go/transport/internal/auditconv`, as the embedded
client does.


## Persistence

The durable trail is `audit-events.ndjson` in the data directory: one JSON
object per line, each carrying the hash of its predecessor. It is **node-local
and not Raft-replicated** — each node records what it did, so reconstructing a
cluster-wide trail means collecting from every node, for this sink alone. (A
second, separate mechanism — the replicated audit chain below — IS
Raft-replicated and per-tenant opt-in via `AllowReplicatedAudit`: an entitled
tenant's events reach BOTH the node-local sink above and the replicated
chain, and the replicated chain's copy reads identically from any node in
the group. The statement above still holds for the node-local sink itself,
and for every tenant that has not opted into the replicated chain.)

`ChainFileLogger` rotates at its size bound and, when it opens, deletes
archives older than its retention window (90 days by default). Retention is
therefore a **deletion deadline, not a backup**: nothing copies an archive
anywhere before it is removed.

## The replicated chain (cluster-wide)

See [architecture.md](architecture.md#cluster-wide-replicated-trail) for the
design rationale and [Status of the replicated chain](architecture.md#status-of-the-replicated-chain)
for exactly what has and has not shipped. This section is the implementation
map.

**Package layout: the apply-time mechanism.**

| File | Contents |
|---|---|
| `pkg/vlog/table.go` | `TablePrefix`, key encoding/decoding, head record codec, `MaxPrunePerCommand`; `LogTablePrefix` and the named-log record keys; `IsTableKey` (both prefixes) |
| `pkg/vlog/log.go` | `vlog.Chain` — the apply-time head: `Append`'s sequencing and staging, `Prune`, `Restore` (the audit heads and every named log) |
| `pkg/vlog/named.go` | Named logs on the same `vlog.Chain`: `LogID`, `Descriptor`, `Create`, `AppendEntries`, `Checkpoint`, `ExpireBodies`, the RFC 6962 tree over `github.com/transparency-dev/merkle` |
| `pkg/vlog/read.go` | Storage reads every replica answers alike: `ReadHead`, `ReadCheckpoint`, `Get`, `Inclusion`, `Consistency` |
| `pkg/vlog/errors.go` | `TableWriteError`, `TableReadError`, `CorruptTableError`, `InvalidTenantIDError`; the named-log errors (`InvalidLogNameError` … `ExpireNotAllowedError`) |
| `pkg/vlog/request.go` | `HeadRequest`, `GetRequest`, `InclusionRequest`, `ConsistencyRequest` — the named-log `Lookup` query types — and `Read`, which answers them from storage |
| `pkg/audit/chain.go` | `Chain` — the audit instance over `vlog.Chain`: `Append` (event validation, `PrevHash`, the digest fold), `Query`, `Verify` |
| `pkg/audit/chain_errors.go` | `EncodeEventError`, `MissingTimestampError`, `InvalidCursorError`, `TenantMismatchError` |
| `pkg/protocol/audit.go` | `OpAuditAppend`, `AuditSubtypeAppend`/`AuditSubtypePrune`, `EncodeAuditAppend`, `EncodeAuditPrune`, `DecodeAuditCommand` |
| `pkg/protocol/vlog.go` | `OpVerifiableLog`, `VerifiableLogSubtypeCreate`/`Append`/`Checkpoint`/`ExpireBodies`, their `EncodeVerifiableLog*` and `DecodeVerifiableLog*` codecs, `DecodeVerifiableLogCommand` |
| `pkg/cluster/featuregate/featuregate.go` | `AuditAppend` and `VerifiableLog` features |
| `pkg/statemachine/vlog_apply.go` | `ApplyVerifiableLog` — the one `OpVerifiableLog` apply both families dispatch to, and its refusal-versus-halt rule (`isLogTableFault`) |
| `pkg/engine/mvcc_engine.go` | `MVCCEngine.handleAuditAppend`, `handleVerifiableLog`, `WithMVCCLogs` (attaches the state machine's `vlog.Chain`; the audit `Chain` is `audit.ChainOn` over it), `resyncAuditChain`, the `audit.QueryRequest`/`audit.VerifyRequest` and `vlog.Request` `Lookup` cases |
| `pkg/store/pebble.go` | `PebbleStateMachine.pebbleHandleAuditAppend`, `pebbleHandleVerifiableLog`, `pebbleAuditWriter`, `SetLogs`/`restoreAuditChain`, `resyncLogs` (rebuilds the registry from storage after a failed update), the same three `Lookup` cases |
| `pkg/store/factory.go`, `pkg/store/mvcc.go` | `WithLogs` / `SetLogs` / `Logs` / `AuditChain` accessors mirroring the lessor's; `MVCCStateMachine.resyncLogs` |
| `pkg/store/errors.go` | `LogsResyncError` — a failed update whose registry could not then be rebuilt, carrying both failures |
| `pkg/server/tenant_host.go` | `newDataPlaneCreateFunc` builds a `vlog.Chain` per tenant and database state machine, as `cmd/qrdbd/start.go`'s `buildSMCreateFunc` does per node group |
| `pkg/store/storetest/logs.go` | `RequireOwnLogsPerStateMachine` — the two-groups proof that no two state machines from one create function share a registry, run over every create function above |

**Package layout: the entitlement, the proposer, and the read API.**

| File | Contents |
|---|---|
| `pkg/audit/query_request.go` | `QueryRequest`, `VerifyRequest` — the `Lookup` query types the apply-time dispatch answers |
| `pkg/quota/types.go` | `Entitlements.AllowReplicatedAudit` |
| `pkg/tenant/service.go` | `TenantService.ReplicatedAuditEnabled` |
| `pkg/service/audit_shard_resolver.go` | `AuditShardResolver`, `AuditIsolationLookup`, `SystemShardAuditResolver` |
| `pkg/service/audit_replicated_logger.go` | `ReplicatedLogger` (`Emit`/`Query`/`VerifyTenantChain`), `routingTenant` |
| `pkg/service/audit_optin_logger.go` | `AuditEntitlementLookup`, `OptInAuditLogger`, `TenantChainVerifier` |
| `pkg/service/audit_errors.go` | `AuditFeatureNotActiveError`, `DedicatedTenantAuditNotSupportedError`, `ReplicatedAuditNotEntitledError` |
| `pkg/server/lifecycle/lifecycle.go`, `audit_replicated.go` | `Node.ReplicatedAudit`, `Infra.ReplicatedAuditPlatformEnabled`, the lazy `nodeAuditIsolationLookup`/`nodeAuditEntitlementLookup` adapters `BuildServices` binds before `Node.Tenant` exists |
| `pkg/api/{rest,grpc,quic,mcp,unix}` | the replicated query/verify capability per transport (`handleAuditQuery`'s `replicated` branch, `handleAuditVerifyReplicated(Chain)`) |
| `sdk/go/transport/*`, `sdk/go/node/node.go` | `AuditFilter.Replicated`, `AuditClient.VerifyReplicatedChain`, `ChainContinuityResult`, the embedded transport's `WithReplicatedAuditLogger` |
| `cmd/qrdb/audit.go` | `--replicated` on `qrdb audit query` / `qrdb audit verify` |

**Reachability from a composer.** `sdk/go/node` forwards the node's own
`Node.ReplicatedAudit` to REST, gRPC, QUIC, Unix and MCP as well as the
embedded transport (`server.WithReplicatedAuditLogger`, surfaced as
`CryptoOptions.ReplicatedAuditLogger`), so `--replicated` query and chain
verification answer the replicated chain on every transport. A logger the
caller passes as a server option comes last and replaces the node's.

**Error classification at apply time.** `Chain.Append`/`Chain.Prune` errors
split into exactly two buckets, and the handler on both engine families
makes the identical split:

- `*vlog.TableWriteError` — a local storage fault staging the write into
  the apply batch. Returned as a Go `error` from the handler, which HALTS
  apply: the fault is local to this replica, not a property every replica
  would reach identically, so answering it as a refusal would let this
  replica silently diverge from peers whose write succeeded.
- Everything else `Append` can return (`*audit.MissingTimestampError`,
  `*audit.EncodeEventError`, `*audit.TenantMismatchError`,
  `*vlog.InvalidTenantIDError`) and everything a malformed or
  unrecognized command can produce (`*protocol.InvalidCommandError`) is a
  deterministic consequence of the committed entry's own bytes — every
  replica reaches it identically — so it is encoded as an application
  refusal (`statemachine.RefusalResult`) via `pkg/statemachine`'s
  `refusalRules` table, never returned as a Go error. `Prune` has no
  equivalent per-replica-refusal case at all: every error it can return is
  a fault in this replica's own already-committed storage, so it always
  halts.

Regression tests pin both engine families to this split, and pin
`*vlog.TableWriteError` as the only halting `Append` error.

**Reserved keys are refused at every compare.** A compare's
result (`CompareResultLess`/`Greater`, or simply whether it held at all) is
observable to the proposer regardless of outcome, so evaluating one against
a reserved key (lease, audit or node-local table) would make an ordinary
transaction or guarded write an extraction oracle over state no caller may
read directly, and a version compare alone would leak whether a reserved key
exists. One shared predicate, `statemachine.IsReservedKey`, is enforced by
`statemachine.ReservedKeyRefusal` at every op key (`statemachine.OpKeys`) and
every compare key (`statemachine.CompareKeys`) on `MVCCEngine`, `KVEngine`
and `PebbleStateMachine` alike. Every combination
(transaction/guard compare × lease/audit/node-local key × engine family) is
tested in `reserved_key_compare_test.go` in both `pkg/engine` and
`pkg/store`.

**Unconditional attachment, not an opt-in flag.** `cmd/qrdbd/start.go`'s
`buildSMCreateFunc` and `pkg/server/lifecycle.NewLocalEngine` attach a
`*vlog.Chain` (the audit `Chain` is `audit.ChainOn` over it) to every
`mvcc`/`pebble` state machine they build, with no configuration surface that
could omit it — see
[architecture.md's Status section](architecture.md#status-of-the-replicated-chain)
for why a conditional attachment would be a cluster-state-divergence bug,
not a degraded feature.

**One chain per state machine, by construction.** The create function
`buildSMCreateFunc` returns builds a fresh `vlog.NewChain()` for every state
machine instance it creates, and `NewLocalEngine` one per call; the daemon
keeps no node-wide `Chain`. `vlog.Chain.Restore` replaces the whole head table, so
a `Chain` shared by two Raft groups would interleave their sequence numbers
and let one group's open or recovery overwrite the other's heads.
`cmd/qrdbd/audit_chain_per_sm_test.go` starts two groups from one create
function, appends to each, reopens each in turn, and asserts that every
group's sequence continues from its own storage alone.

## Test strategy

**Integration target:** `make integration-test-audit` (no Docker; selection in `test/integration/features.mk`). It runs the audit query/verify CLI suite per protocol and the tenant-confinement suite. Per-transport accounting is the `audit` row of `test/integration/COVERAGE.md`.

- **Every sink's `Emit` error path has a test**, including the three that can
  never fail — each of those asserts the *reason* nil is the truthful answer,
  not merely the value.
- **The fan-out reaching every sink is the critical test** — a
  stop-at-first-error implementation would pass a naive "the aggregate returned
  an error" assertion and lose the trail in production, in the healthy sinks.
- **The fail-closed proof is end-to-end, for both gates**: a real
  `ChainFileLogger` on a real file, faulted at `os.File.Write`, driven through a
  real HTTP request, is asserted to produce HTTP 503 carrying a
  `*audit.GateError` that unwraps to an `*audit.WriteError` naming the file —
  and the handler is asserted not to have run. The SO half additionally drives a
  **real `*auth.SOAuthenticator`** over a real `*kvstore.MemoryKVStore` with a
  real committed PIN, because "the SO factor passed and the handler still did
  not run" is not observable against a stub verifier. A test that merely
  constructs those error types proves the constructors work and says nothing
  about whether any request produces one.
- **Every refusal test has a control with a healthy sink.** A gate that refused
  everything unconditionally would otherwise pass the whole suite.
- **The deliberate non-gate is pinned too.** `SetSOPIN` with a faulted sink is
  asserted to report success *and* to have really committed the PIN, so a later
  reader does not "fix" a post-commit record into a gate.
- **The default gets its own assertion** at three layers — the zero
  `AuditFailurePolicy`, the zero `wiring.AuthMiddlewareConfig` field, and the
  omitted YAML key — because the default *is* the security property.
- **The chain is verified ACROSS a fault, not just at it.** A test emits, fails
  the fsync, emits again healthy, and calls `VerifyChain`; another does the
  same across a close-and-reopen. A single-emit test that asserts only the
  returned error cannot see a desynchronised `prevDigest` and will pass over
  the bug.
- **The chain is verified ACROSS A ROTATION, on both a real rotation and a
  real restart.** Every rotation-boundary test drives the real
  `ChainFileLogger` over a real `rotate()` — `rotateFunc` is never installed
  there, because it replaces the rotation wholesale and would delete the anchor
  write, the rename and the reopen, which is the entire subject. The honest
  direction verifies **every** file the run produced, not a file count; the
  tampering direction edits a record in a middle archive, deletes one, truncates
  one, duplicates one and re-seats one, and asserts the type and the file named
  in each case.
- **Fsync and rotation failures use an unexported fault seam** on
  `ChainFileLogger` (`writeFunc` / `syncFunc` / `rotateFunc` / `renameFunc`,
  set only by tests in `pkg/audit`). They cannot be induced portably, and the
  `chmod` trick that fakes a rename failure is a no-op under a root CI runner.
  `renameFunc` is the narrow one: it replaces **only** the rename, so the
  anchor write, the close, the reopen and the failure accounting still run.
  That matters because a failed rename is the one rotation failure with a
  consequence for the *chain* rather than only for the size bound, and
  `rotateFunc` — which replaces the rotation wholesale — would delete the
  entire subject.
- **Chronological ordering** on a prefix scan is tested, since it is what the
  query surface depends on.
- **Error tests** per function.

## Observability

The audit log **is** the observability surface for security-relevant events,
with one deliberate exception: the audit plane cannot observe its own failure,
so `qrdb_auth_audit_write_failures_total` and the `ERROR` line beside it carry
that one fact on planes that do not depend on it. Everything else is
[../api-server/](../api-server/)'s concern.
