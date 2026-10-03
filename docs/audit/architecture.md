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
- **Storing audit entries in the KV store at all.** Rejected. The trail is a
  hash-chained append-only file plus an in-memory ring; it is not in the
  keyspace, user or internal, so no KV surface can enumerate, rewrite or
  compact it. A tamper-evident chain wants an append-only medium, and a
  replicated mutable store is the opposite of one.
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
| MCP | `UNAVAILABLE` (−32006) + text: `authorization unavailable, retry: …` | `PERMISSION_DENIED` (−32003) + text: `authorization denied: …` |

**Both named exclusions are gone**, and how each closed is worth recording:

- **MCP used to carry no status code at all** — tool results were text, so it
  could not agree with the other three mechanically and the distinction lived
  in the wording alone. It has an error vocabulary now, and the MCP gate
  renders `ClassifyAuthError`'s answer through it rather than a coarser split
  of its own, so all four transports map the same class the same way. The
  wording is unchanged and still matters: it is what an operator reads.
- **The QUIC wire had no rate-limit status**, so a throttled caller rode
  `StatusUnavailable` — the right instruction, retry, without the reason. The
  wire declares `StatusResourceExhausted` now.

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

The hash chain makes a back-filled record detectable **across the whole trail**,
archives included, and that is a control the system runs rather
than only a property an auditor's own tooling could check: `pkg/audit.Verify`
is the single canonical entry point (type-asserting the configured
`AuditLogger` to the `Verifier` interface `*ChainFileLogger` implements, so an
`aggregateLogger` delegates to whichever child holds the chain), and every
transport handler and the CLI's `qrdb audit verify` call through it — see
[api.md](api.md) for the transport matrix and [cli.md](cli.md) for the
command. What it detects is described under *The chain across a rotation*
below.

## The chain across a rotation

**The chain spans the rotation. It does not restart per file.**

When the active file reaches its size bound the sink appends a `chain-anchor`
record to it, renames it to an archive, and opens a fresh file. The first
record written into the new file carries the **same** digest the anchor just
recorded, so the boundary is stated from both sides and the running digest
continues uninterrupted.

That is what the writer has always produced. Until the rotation-boundary fix
the *reader* assumed each file began at zero, so every file opened after a
rotation was reported broken at index 0 on entirely honest data — the one
signal this subsystem exists to make trustworthy, firing on healthy archives.
The same assumption in the restore path (`initPrevDigest`) broke the chain for
real at the first write after a restart, because the active file it folded from
zero had begun mid-chain.

Verification is now two properties, checked in order:

1. **Each file is internally sound.** Every record's `PrevHash` is the fold over
   its predecessor, seeded with the digest the file's own first record declares.
   This catches any edit, insertion, reordering or truncation inside the file.
   The terminating anchor pins an archive's *last* record, which a self-contained
   per-file chain could not do.
2. **The files form one chain.** Each file's declared seed must be some other
   file's handoff. This is what makes the seed unforgeable, and it is what
   detects a whole archive being deleted, duplicated or reordered — a fault no
   per-file check can see, because every remaining file still verifies perfectly.

The two report different types, because the remediation differs: a
`ChainBrokenError` means a **record** was changed and names the file and index;
a `ChainLinkError` means a **file** was removed, duplicated or reordered.

**The alternative was rejected deliberately.** Making each file self-contained —
rotating before the outgoing record's `PrevHash` is fixed — is simpler, and it
gives up the cross-file evidence the data already carries: an adversary who can
edit a line can delete a file, and under per-file chains deleting an entire
archive leaves a directory in which everything that remains verifies. It would
also make the anchor decorative, and it would put a second marshal on the write
path that every authorization decision now traverses.

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

## Who may read which events

The query sinks are **node-local and shared by every tenant**: one chain file
and one ring hold the events of every tenant on the node. A query therefore
never reaches a sink with a tenant the caller chose. Before a logger is read,
every transport — REST, gRPC, the unix socket, QUIC, MCP and the embedded
client — confines the filter to the **caller's own tenant**
(`tenant.ConfineAuditFilter`):

| The request names | The query reads |
|---|---|
| no tenant | the caller's own tenant's events |
| the caller's own tenant | the caller's own tenant's events |
| any other tenant | nothing: refused (403, `PERMISSION_DENIED`, `StatusPermissionDenied`, the permission-denied MCP tool code, `*tenant.CrossTenantDeniedError` on embedded) |

**The rule has no exception for the system tenant.** An identity in the system
tenant holding `audit:query` reads the system tenant's events and the
**platform events** — those that carry no tenant at all, such as the node's own
inspection opt-out. It is refused every other tenant's events, exactly as a
tenant Admin is refused the system tenant's and its neighbours'.

**An event of a tenant-scoped flow names its tenant.** Every share-holder event
-- a holder's registration, envelope claim and quorum submission, a rekey
start and a revocation, refusals included -- carries the tenant that owns the
holder's scope: a tenant barrier's holders their tenant, a database barrier's
holders the database's tenant, and the system and recovery holders the system
tenant. That tenant's auditors read them and nobody else's do. A submission
naming a holder that does not exist is recorded in the scope it was made to,
so it is the scope's tenant's too.

**Nothing else in the filter can widen it.** The tenant match is applied to
every event before the page is cut, so the time range, the actor, resource,
action and outcome filters, the page size and the cursor only narrow the
result: a cursor is a position in the caller's own matching events and cannot
step into anyone else's. The flag that admits platform events
(`audit.Filter.Untenanted`) is never decoded from a request; only the
confinement sets it, and only for a system-tenant caller.

**The caller is who authenticated.** On the network transports that is the
identity the authentication middleware bound from the client certificate or
token. The embedded transport has no middleware, so it reads the identity in
the call's context, or the tenant bound to it when there is no identity, and an
unbound context is the system tenant — the in-process host.

**BREAKING:** before this rule the query honoured whatever `tenant_id` the
request carried, and an empty one read every tenant's events, so a tenant Admin
holding `audit:query` read the whole node's trail.

## The trail exists before the sealed window

**The node opens its compliance trail before it builds its barrier**, and
does not start without it. A Shamir-sealed node gathers its unseal quorum in
the sealed window -- before the data plane, the key-value store or any
tenant's store exists -- and holders claim their envelopes there on first
boot. Those are among the most security-relevant events the node has, so they
are recorded on the same trail as everything else, not on a second one, and
not later.

The daemon therefore opens the trail -- the hash-chained file, the ring and
the operational log -- ahead of the seal subsystem, for the life of the
process: every boot iteration, an in-process encryption migration's included,
records on the one trail. Opening it fails start **closed**, exactly as it did
when the trail was opened after unseal: a trail that cannot be opened stops
the node before any barrier is built or any sealed window served.

**The process-inspection opt-out is still the trail's first record.** It is
written the moment the trail exists -- now before the seal subsystem rather
than after unseal -- and a node that cannot record it does not run.

The SDK node records on the trail its caller supplies (`server.WithAuditLogger`),
which exists before the node does, so its sealed window is recorded too.

### Share-holder events

Every holder scope -- system, recovery, tenant and database -- records its
holder events on the trail through the one holder engine, keyed by the scope's
owning tenant. Each names the scope, the holder and the outcome; quorum
events name the operation. **None carries a share, an envelope, a PIN, a
claim signature, a factor response or a key**, and a refusal's reason is a
fixed token, never an error's text.

**They follow the node's one audit failure posture.** A registration, a claim
and a rekey opening are recorded as `permitted` before they take effect and,
under `auth.audit_failure_policy: "fail-closed"` (the default), refused when
the trail cannot record them -- the holder is not enrolled, the envelope stays
held and claimable, no round opens -- exactly as the authorization and SO
gates refuse. Once the operation has run, a second record carries its
`success` or `failure`; a gating record never says `success`, so the
hash-chained trail cannot hold one for an effect that did not happen.
Refusals, quorum submissions and revocations are post-hoc
records: the operation has already been refused or completed, so a trail
that refuses one is counted on
`qrdb_audit_write_failures_total{component="share-holder"}` and logged.

| Event | Recorded when |
|---|---|
| `holder-register` | a holder is enrolled, or the enrolment refused |
| `holder-claim` | a holder claims its envelope, or the claim is refused |
| `holder-quorum-submit` | a share submission's holder is authenticated, or refused |
| `holder-rekey-start` | a rekey round opens, a revocation's included, or is refused |
| `holder-revoked` | a revoked holder's record is removed, once the rekey is durable |
| `holder-unenrol` | a holder is removed before the barrier is initialized, or the removal refused |

### The replicated in-tenant trail

The trail is node-local. A replicated trail held inside each tenant -- records
appended through the replicated store rather than to one node's file -- is
planned, and is not part of this feature. When it exists it cannot take the
sealed window's records directly: the store it appends through is behind the
barrier that window is opening. Those records go to the node's trail as they
happen, as they do today; the replicated trail can carry them by buffering
the window's events in memory and appending them once the barrier is
unsealed. Nothing here buffers them, and nothing here depends on that.

## Trust boundaries

- **Audit entries are not in the key-value store**, so no KV verb reaches
  them: the trail is `audit-events.ndjson` plus the in-memory ring.
- **The audit file is mode 0600.**
- **`PrevHash` chains entries** so a deletion or edit is detectable rather than
  silent.
- **Permission-denied events are themselves audited** (`auth.denied`, result
  `denied`) — a failed attempt is exactly what a trail exists to record.
- **An authorization decision that could not be recorded is refused** by
  default. The audit record is part of the decision, not a side effect of it.
- **The same is true of the SO second factor.** The two gates share one posture
  value; there is no separate SO setting.
- **Querying the trail requires `audit:query`**, distinct from any operational
  permission, **and reads only the caller's own tenant's events** — see
  [Who may read which events](#who-may-read-which-events).

## Correction notice

Earlier revisions of this folder described a `kvLogger` / `fileLogger` /
`multiLogger` / `noopLogger` factory selected by an `audit.Config` carrying
`kv_backend` / `file_backend` keys, writing to `__sys:audit/{tenantID}/
{timestamp_nano}`. **No such factory, config struct, config block or KV
backend has ever existed**, and `pkg/audit`'s own package comment repeated the
claim until it was corrected alongside this folder.

The canonical names are the ones in the table above, composed at the node's
wiring site rather than selected by a factory. The correction is now applied
throughout the folder rather than flagged in place, and the one consequence
worth restating is that the old text was not merely stale but misleading in the
operator's direction: it said querying required a backend that does not exist,
when `audit query` in fact works with no configuration at all.

## Known limits of chain verification

These bound what the *tamper-evidence* claim above is worth today. None is
introduced by the fail-closed work.

- **Verification is on-demand, not continuous.** `qrdb audit verify` and its
  Tier-1 equivalents on every transport run the check when
  something calls them; there is still no startup check, no periodic scan,
  and no metric that fires on a break without a human or a scheduled job
  invoking the verb. A tampered trail sits undetected between runs of
  whatever calls it.
- **Removing a contiguous prefix of the archives is indistinguishable from
  retention.** Retention pruning deletes the oldest archives by age, so a
  healthy trail's walk routinely starts mid-chain, and no hash walk can tell
  that apart from someone deleting the same files. It is therefore **reported,
  not refused**: `VerifyChainSet` returns `Genesis: false` and names the digest
  the surviving head claims to continue from. Anchoring the head would need
  evidence kept outside the trail — an operator-recorded high-water digest, or
  shipping the trail off the node — and this feature ships neither.
- **The last record of the active file is not pinned until another follows
  it.** A chain cannot vouch for its own tip. This is inherent to an
  append-only chain and is unchanged by the rotation work; every archive's last
  record *is* pinned, by the anchor that terminates it.
- **A record that will not parse is skipped, not folded.** A torn line is not a
  record, and the writer's restore path and the verifier agree on that, so a
  crash mid-write leaves a trail that still verifies.
- **At most one full-trail scan runs per logger at a time.** A rotation set
  only grows, so concurrent callers gain nothing from each walking it
  independently; a second call arriving mid-scan is refused immediately with
  `*VerificationBusyError` rather than starting its own scan or being queued
  behind the first. The caller's context is honored throughout the walk —
  checked per record within a file and per file within the set — so a
  canceled or expired context stops the scan promptly and reports
  `*VerificationCanceledError`, not a tamper verdict.

Treat a clean `qrdb audit verify` as **evidence as of the moment it ran**, not
as a standing guarantee between runs — the remaining gap above is what a
startup check or a scheduled scan would close, and neither exists yet.

## What this feature deliberately does not do

- **No log shipping.** There is no syslog, no remote sink, no agent. The file
  backend produces line-delimited JSON; shipping it is somebody else's job.
- **No archiving off the node.** The file sink prunes -- rotated archives older
  than its retention window (90 days by default) are deleted when the sink
  opens -- so retention is a deletion deadline, not a backup. Moving archives
  somewhere durable before that deadline is the operator's job.
- **No alerting.**
- **No redaction.** Callers must not put secret material in `Attributes` —
  values are never included by the subsystems that emit, and nothing strips them
  if a caller adds one.
