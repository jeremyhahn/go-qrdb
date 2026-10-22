# Audit — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-22
> Conflicts with this document are defects. Fix them here first.

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
when it fails the record still lands and still fsyncs. Reporting it would make
the middleware refuse requests whose audit entry had in fact persisted, on
*every* request while the size bound stays exceeded. Rotation failures ride `qrdb_audit_rotation_failures_total` and an `ERROR` log line
instead. See [engineering.md](engineering.md#the-emit-error-contract).

**The fan-out reaches every sink.** `aggregateLogger` attempts all of them even
after one fails, and returns the first error. A single backend failure must not
suppress the trail in the healthy ones.

### Alternatives rejected

- **Per-subsystem logging.** Rejected — one interface, one schema, one query
  surface.
- **An `Emit` with no error return.** Rejected: it decides the compliance
  question for every caller. A caller that wants to fail closed cannot.
- **A parallel `EmitErr` beside `Emit`.** Rejected. Two methods means two
  behaviours to keep in step across every implementation, and the unchecked one
  stays the easy one to call.
- **Failing fast in the fan-out.** Rejected: stopping at the first failed sink
  would let a broken file destination silently blank the ring buffer and the
  operational log stream that were both perfectly healthy.
- **Storing audit entries as KV keys.** Rejected. The node-local trail is a
  hash-chained append-only file plus an in-memory ring, and the opt-in
  replicated chain is a reserved raw-keyspace table (see *Record layout*
  below); neither is a key any KV surface can enumerate, rewrite or compact.
  A tamper-evident chain wants an append-only medium, not a mutable keyspace.
- **A world-readable audit file.** Rejected: mode 0600.

## Fail-closed on audit write failure

**A gating decision is not made unless its audit record persisted.**

There are **two gates**, and they run on **one posture**:

| Gate | What it asserts | Component |
|---|---|---|
| `AuthMiddleware.authorize` | the RBAC/ABAC decision | `authorize` |
| `SOGate` (`SOMiddleware` on REST) + `auth.SOAuthenticator.VerifySOPIN` | the security officer's second factor (PIN) | `so-dual-factor`, `so-auth` |

They are **two distinct assertions about the same request**, and both are
gated: the sink can fail between them, and an
SO operation — rekey, custodian enrolment, CSR signing — is the most privileged
action the system offers.

When either gate cannot record its event, it **refuses the request** —
`*audit.GateError` wrapping the sink's `*audit.WriteError`, which the
transports answer as HTTP 503 / gRPC `Unavailable`. `GateError.Component` says
which gate refused.

**One type, one posture, one lever.** The posture is `audit.FailurePolicy` and
it lives in `pkg/audit`, not in `pkg/auth/middleware`: `pkg/auth/middleware`
imports `pkg/auth`, so a policy owned by the middleware could not be read by the
SO factor. A second configuration key would split the posture.

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

MCP renders `ClassifyAuthError`'s answer through its own error codes rather
than a coarser split, so all four transports map the same class the same way;
the text is what an operator reads. A rate-limited caller is
`StatusResourceExhausted` on QUIC, not `StatusUnavailable`.

This matters most on the **tenant-barrier surface**, which is what an operator
and the SDK use to *recover* a node: a node whose audit disk is full must report
its unseal endpoint as unavailable, not as an authorization failure, which every
client treats as terminal.

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
  other gate — `component="so-auth"` and `component="so-dual-factor"`,
- an `ERROR`-level line on the operational log stream.

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

A reader therefore never assumes a file begins at zero: neither verification nor
the restore path (`initPrevDigest`), which folds the active file from the seed
its first record declares.

Verification is two properties, checked in order:

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
path that every authorization decision traverses.

## Event schema

The event (`audit.AuditEvent`, `pkg/audit/audit_event.go`) carries: `Timestamp`
(UTC), `Actor` (certificate CN, JWT `sub` or API key), `TenantID`, `Role` at the
time of the event, `Resource`, `Action`, `Outcome` (`success`,
`failure:<reason>`, `denied:<reason>`), `Attributes`, `ClientIP`, `RequestID`,
and **`PrevHash`** — the hex SHA-256 digest of the previous event, empty for the
first, forming a **tamper-detection chain**.

Recording the actor's **role at event time** matters: a role changed afterwards
must not rewrite what the actor was allowed to do then.

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
records on the one trail. Opening it fails start **closed**: a trail that
cannot be opened stops the node before any barrier is built or any sealed
window served.

**The process-inspection opt-out is the trail's first record.** It is written
the moment the trail exists, before the seal subsystem, and a node that cannot
record it does not run.

The SDK node records on the trail its caller supplies (`server.WithAuditLogger`),
which exists before the node does, so its sealed window is recorded too. The
desktop's embedded server keeps the same trail when it is enabled
(`EmbeddedConfig.Audit`, off by default), and so does the SDK's flat
`standalone.NewServer` (`ServerConfig.AuditTrail`, off by default), on the same
rules: opened before the seal barrier, confined to the data directory -- a
symbolic link in it cannot carry the trail outside -- and a configured trail
that cannot be opened stops the start. No composer is left without one.

### Barrier events

The barriers record on the same trail, not a second one: the system barrier,
every tenant barrier and every database barrier, through the adapter
`seal.NewComplianceAuditLogger`, keyed by the owning tenant and naming the
barrier. A Shamir unseal therefore leaves the holders' submissions and the
barrier's `quorum-complete` and `unseal` side by side. **Each event has one
emitter:** a submission and a rekey opening are the holder engine's records,
and the barrier emits no copy. The list is in
[../barrier-seal/engineering.md](../barrier-seal/engineering.md#observability).

### Share-holder events

Every holder scope -- system, recovery, tenant and database -- records its
holder events on the trail through the one holder engine, keyed by the scope's
owning tenant. Each names the scope, the holder and the outcome; quorum
events name the operation. **None carries a share, an envelope, a PIN, a
claim signature, a factor response or a key**, and a refusal's reason is a
fixed token, never an error's text.

**They follow the node's one audit failure posture.** A registration, a claim,
a rekey opening and an unenrolment are recorded as `permitted` before they take effect and,
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

The sink described above is node-local: `ChainFileLogger`'s chain lives on one
node's disk, and the ring buffer is one node's memory. A SECOND, independent
mechanism -- the cluster-wide replicated trail below -- also exists,
apply-time-assigned and Raft-replicated rather than written by one process.
The two are not alternatives: the node-local sink records unconditionally, and
the replicated chain is additional, **opt-in** infrastructure layered on top of
it (see [Status](#status-of-the-replicated-chain) below) --
`lifecycle.BuildServices` composes the two through `audit.NewAggregateLogger`, so every ordinary `Emit`
call reaches both, and the replicated child is a no-op for a tenant whose
entitlement is off.

**The sealed window does not carry records into the replicated chain.**
It cannot take them directly -- the store the chain appends through is behind
the barrier that window is opening, and before `BuildServices` runs there is
no `Host` to resolve a shard against at all -- so holder/ceremony events made
during the sealed window are node-local only. Carrying them over would be a
buffer-then-append on the proposing side once the data plane exists, not
something `Chain.Append` does.

## Cluster-wide replicated trail

### Status of the replicated chain

The mechanism that lets a Raft group agree on one hash-chained audit trail per
tenant:

- `pkg/audit.Chain` — the deterministic, apply-time authority for every
  tenant's chain (`Append`, `Prune`, `Query`, `Restore`), the audit instance
  of `pkg/vlog.Chain`, which owns the head, its sequencing and the table.
- `pkg/vlog.TablePrefix` (`pkg/vlog/table.go`) — the reserved raw-keyspace
  table the chain is durable in, mirroring `pkg/lease`'s table exactly.
- `protocol.OpAuditAppend` — the single opcode carrying both an append and a
  retention prune, gated by `featuregate.AuditAppend` so no replica of a
  group is ever sent an opcode a sibling cannot apply.
- `protocol.OpVerifiableLog` — the named-log opcode (`pkg/protocol/vlog.go`),
  one sub-type each for create, append, checkpoint and body expiry, gated by
  `featuregate.VerifiableLog` on the same terms. Both families hand it to
  `pkg/statemachine.ApplyVerifiableLog`, which refuses what every replica
  refuses alike (a malformed body, a missing or existing log, a mismatched
  checkpoint, a state machine with no registry) and halts on an unknown
  framing or a fault in this replica's own table. `OpAuditAppend` writes no
  Merkle node.
- Both state-machine families (`pkg/store.PebbleStateMachine`,
  `pkg/engine.MVCCEngine`) dispatch the opcode to `Chain`, restore it on open
  and on snapshot recovery, and resync it after a failed apply, as the lease
  table does. The resync and the restore cover the named
  logs too, so a replica opened from disk or from a snapshot taken mid-log
  serves the same heads and proofs as its peers and appends the same nodes.
- Every `mvcc`/`pebble` state machine this binary composes — the Raft
  factory in `cmd/qrdbd/start.go`, the tenant and database groups'
  `newDataPlaneCreateFunc` in `pkg/server`, and the standalone/embedded
  composition in `pkg/server/lifecycle.NewLocalEngine` — attaches a `Chain`
  unconditionally.
  There is no configuration flag that turns this attachment off, because a
  replica missing it would answer `OpAuditAppend` with a refusal while a
  sibling that has one applies the write: state divergence within one Raft
  group, not a degraded feature.
- Each state machine gets a `Chain` of its own, built with it; no `Chain` is
  shared between state machines, even two Raft groups on one node built by
  the same factory. `Chain.Restore` replaces the whole head table with what
  one state machine's storage holds, so a shared `Chain` would number one
  group's events after another's, and opening or recovering either group
  would overwrite the other's heads.

What proposes `OpAuditAppend`, and how a caller reads back what it proposed:

- `quota.Entitlements.AllowReplicatedAudit` — the per-tenant opt-in. False
  (the secure default) for every tenant unless an explicit
  `tenants:set_entitlements` grant or the operator's configured default
  entitlements turn it on; see [Opt-in, not default-on](#opt-in-not-default-on)
  below.
- `tenant.TenantService.ReplicatedAuditEnabled` — resolves that entitlement,
  with the system tenant handled separately (see "Platform events" below).
- `service.SystemShardAuditResolver` — names the `(Host, shardID)` that
  serves a tenant's chain. Every tenant that is not dedicated-isolation, and
  an unregistered tenant id, resolves to the system shard; a
  dedicated-isolation tenant (volume/raft) is refused with
  `*service.DedicatedTenantAuditNotSupportedError` rather than silently
  guessing, because that tenant's chain belongs on its own dedicated host,
  which this resolver does not reach.
- `service.ReplicatedLogger` — the `audit.AuditLogger` that actually encodes
  and proposes `protocol.OpAuditAppend` from an ordinary `Emit` call (and
  answers `Query`/`VerifyTenantChain` by reading back through
  `audit.QueryRequest`/`audit.VerifyRequest`). It refuses with
  `*service.AuditFeatureNotActiveError` before proposing when
  `featuregate.AuditAppend` has not activated on the target shard.
- `service.OptInAuditLogger` — wraps `ReplicatedLogger` behind the
  entitlement check above, so an unentitled tenant's `Emit` is a complete
  no-op (never reaches Raft) and its `Query`/`VerifyTenantChain` answer
  "not answerable here" (`Total: -1` / `*service.ReplicatedAuditNotEntitledError`)
  rather than a misleading empty-but-real page or a trivially-true report.
- `lifecycle.BuildServices` wires `OptInAuditLogger(ReplicatedLogger(...))`
  into every composition's `audit.NewAggregateLogger` fan-out alongside the
  node-local sink, and exposes it separately as `Node.ReplicatedAudit` for the read API
  below. The isolation and entitlement lookups it binds `SystemShardAuditResolver`
  and `OptInAuditLogger` to are resolved LAZILY against `Node.Tenant`, because
  the resolver is constructed before `Node.Tenant` is populated later in the
  same call -- see `pkg/server/lifecycle/audit_replicated.go`.
- A read API and CLI surface over `Chain.Query` and the `Chain.Verify`
  hash-continuity check, on every transport -- REST, gRPC, Unix, QUIC, MCP,
  the embedded transport and the Go SDK. See
  [api.md](api.md#the-replicated-chain-read-surface) and
  [cli.md](cli.md#--replicated).

### Opt-in, not default-on

The entitlement defaults to **false**: `quota.DefaultEntitlements()` leaves
`AllowReplicatedAudit` at its zero value. The compliance framing (FedRAMP, PCI
DSS, HIPAA all require a trail) does not argue for default-ON, because:

- The node-local trail is unconditional and already satisfies the compliance
  requirement by itself (see "Persistence" in [engineering.md](engineering.md)).
  The replicated chain is additional evidence -- cluster-wide, Raft-replicated,
  independently queryable from any node -- not the thing that makes a
  deployment compliant in the first place.
- Every tenant's replicated writes ride the SAME Raft group's log and
  snapshot, permanently, with no retention-driven deletion (unlike the
  node-local file's 90-day default): turning it on for every tenant by
  default would grow that cost for deployments that never asked for it.
- A hardened compliance reference configuration sets the operator's default
  entitlements so every tenant is covered without a per-tenant grant, which
  is the intended path to "on by default" for a deployment that wants it,
  without changing the built-in software default for everyone else.

**Known limitation:** `OptInAuditLogger.platformEnabled` (the system tenant's own
switch, since the system tenant is never provisioned through the per-tenant
entitlements path) has no operator-facing configuration key:
`lifecycle.Infra.ReplicatedAuditPlatformEnabled` defaults to `false` and
nothing in `cmd/qrdbd` sets it. The system tenant's platform events reach the
replicated chain only for a composer that sets the `Infra` field directly.

### Record layout: a reserved raw-keyspace table, not a `__sys:` key

The table lives under `TablePrefix = "\x00qrdb.audit/"`, in the state
machine's RAW keyspace — the same `0x00` prefix every state machine already
reserves for its own bookkeeping (the last-applied index, the lease table).
This is deliberate, and different from a `__sys:` key: a `__sys:` key is
still an ordinary MVCC key, merely filtered out of listings by convention; a
raw-keyspace key is not a key the KV/MVCC engines expose to `Range`, `List`
or `watch` AT ALL. A tenant enumerating its own keyspace cannot see the
table even by accident, and `statemachine.IsReservedKey` refuses it on every
read and compare path a client could reach (`Lookup`, a transaction compare,
an `OpGuarded` compare), so no compare can serve as an extraction oracle over
the table.

Two record families, one tenant-keyed table, used identically whether the
apply happens in the shared system shard (many tenants multiplexed by the
tenant-ID segment) or a tenant's own dedicated shard (the segment always
names that one tenant):

```
TablePrefix + "h/" + tenantID                           -> head record (seq, lo, digest)
TablePrefix + "e/" + tenantID + "/" + seq (8 bytes BE)   -> marshaled AuditEvent
```

The head record's `lo` field is the lowest surviving sequence number —
see [Retention and pruning](#retention-and-pruning-replicated-deterministic-and-it-does-not-break-the-chain).

Unlike the last-applied index, which is local to one node, this table is
replicated state: it travels in every snapshot and is covered by the
replica hash like any other applied record (`pkg/store`'s
`isNodeLocalKey`), as the lease table is.

The same package keeps a second reserved table beside it, under
`LogTablePrefix = "\x00qrdb.vlog/"`: the named verifiable logs, each a
descriptor, a head, its entries, its Merkle tree nodes and its checkpoints,
keyed by tenant, database and log name. `vlog.IsTableKey` matches both
prefixes, so every reserved-key refusal and the replica hash cover the second
table exactly as they cover this one, and `Chain.Restore` rebuilds both
together: a malformed record in either fails the restore.

### The hash chain stays linear under Raft because it is assigned at apply time

A client never supplies a sequence number or a previous-hash value.
`Chain.Append` reads its OWN current in-memory head, assigns the next
sequence and folds the new event's marshaled bytes (with `PrevHash` set to
the prior head's digest) into a running SHA-256. Because every replica of a
group applies the identical committed log in the identical order, and
`Append` consults only state every replica already agrees on, every replica
computes the byte-identical chain — the same guarantee `lease.Lessor`
depends on for lease IDs.

This is also why `Append` refuses rather than defaults a zero `Timestamp`:
the node-local sink can default one to `time.Now()` because one process
writes the line, but `Append` runs on every replica of the group, and
`time.Now()` is not something two processes ever agree on. A caller that
built the command some other way than `protocol.EncodeAuditAppend` (which
already refuses this before the command is proposed) hits `Append`'s own
backstop and gets `*audit.MissingTimestampError`.

### Retention and pruning: replicated, deterministic, and it does not break the chain

A tenant's chain needs a bound on how much history travels in every
snapshot forever. `Chain.Prune` deletes a prefix of events — `[lo, upper)`
for a caller-supplied cutoff — while leaving the head's sequence and digest
untouched, so the chain keeps extending correctly and a verifier can still
check the surviving tail against the still-live head.

`Prune` reads NOTHING from storage: both bounds come from the `Chain`'s own
in-memory head alone, which every `Append` earlier in the SAME `Update` call has
already advanced, so the result does not depend on how `go-quicraft` packs a
Raft batch (`[append, append, append, prune(3)]` as one `Update` call or as
four). Two further bounds, both clamps on `upper`:

- **Never the newest event.** `upper` is clamped to the head's own `seq`, so
  a chain always keeps at least one live record — an anchor a verifier can
  check the surviving chain against.
- **At most `vlog.MaxPrunePerCommand` (10,000) events per call.** A cutoff
  far ahead of the low-watermark deletes at most that many and reports the
  count; the caller reissues `Prune` with the same cutoff to continue,
  rather than one command staging an unbounded number of deletes into one
  apply batch.

No component proposes a prune: `protocol.EncodeAuditPrune` has no caller
outside tests, so the replicated chain keeps every event. `Prune` is the apply
path only.

### Tenant confinement inside a shared table

`Chain.Query` trusts its `tenantID` argument completely and never reads
outside that tenant's key range — the range bounds passed to the iterator
never leave the tenant's own prefix, so there is no filter to forget. The
file sink's `matchesFilter` (the same `TenantID`/`Untenanted` semantics
`tenant.ConfineAuditFilter` relies on for the node-local trail) is reused, so a platform event filed under the
system tenant's segment with no `TenantID` of its own matches only an
`Untenanted` system-tenant query, never an ordinary one. `Append`, `Prune`
and `Query` alike refuse a tenant ID containing `/` (`*vlog.InvalidTenantIDError`)
— such an id could otherwise carve a second segment inside another tenant's
range in the raw table's concatenated keys.

### Chain hash-continuity verification

`Chain.Verify(r, tenantID)` answers the replicated chain's own version of
"has this tenant's trail been tampered with" — the cluster-wide counterpart
to `pkg/audit.Verify`'s node-local file-chain walk, but a different shape
entirely: one tenant has one Raft-replicated chain, not a rotation set of
NDJSON archives, so there are no files, no order and no handoff digest here.

The check: fold every surviving event for the tenant, in sequence, starting
from the digest the lowest surviving event's own `PrevHash` claims (its
preimage may have been deleted by retention pruning, exactly as a
node-local trail's pruned prefix is unprovable — see below), and compare the
result to the chain's own **authoritative** head digest — the one every
replica derived identically at apply time, which no caller could have
forged. A mismatch, a missing sequence, or a malformed seed is reported as
`Verified: false` with a `Reason`; it is never returned as a Go error,
mirroring `pkg/audit.VerificationReport`'s own "a tamper verdict is a
result, not a failure" rule.

`ChainContinuityReport` is deliberately a different type from
`ChainVerification` (the file chain's result): `Records`, `Head`, `Low` and
`Genesis` describe one tenant's linear sequence, and there is no `Files`,
`Order`, `Seed` or `Handoff` to carry, because there is nothing here shaped
like a rotation set.

**A pruned tenant verifies clean with `Genesis: false`**, exactly as a
retention-pruned node-local trail does: `Chain.Prune` deletes a prefix of
events and leaves the head's digest untouched, so the surviving tail's own
first event's `PrevHash` names a digest whose preimage is gone. That claim
is taken as the fold's starting point, not independently checked — the
terminal comparison against the replicated head digest is what makes this
check meaningful at all, not a claim about the deleted prefix.

The read surface is `service.ReplicatedLogger.VerifyTenantChain`, gated by
the same per-tenant entitlement as `Emit`/`Query`
(`service.OptInAuditLogger.VerifyTenantChain`, refusing with
`*service.ReplicatedAuditNotEntitledError` when the tenant's entitlement is
off), reachable from every transport — see
[api.md](api.md#the-replicated-chain-read-surface) and
[cli.md](cli.md#--replicated).

## Trust boundaries

- **No KV verb reaches an audit entry.** The node-local trail is
  `audit-events.ndjson` plus the in-memory ring; the opt-in replicated chain
  is a reserved raw-keyspace table that `statemachine.IsReservedKey` refuses on
  every client read and compare path.
- **The audit file is mode 0600.**
- **`PrevHash` chains entries** so a deletion or edit is detectable rather than
  silent.
- **Permission-denied events are themselves audited** (action
  `auth.authorize`, result `denied`) — a failed attempt is exactly what a trail exists to record.
- **An authorization decision that could not be recorded is refused** by
  default. The audit record is part of the decision, not a side effect of it.
- **The same is true of the SO second factor.** The two gates share one posture
  value; there is no separate SO setting.
- **Querying the trail requires `audit:query`**, distinct from any operational
  permission, **and reads only the caller's own tenant's events** — see
  [Who may read which events](#who-may-read-which-events).

## Known limits of chain verification

These bound what the *tamper-evidence* claim above is worth.

- **Verification is on-demand, not continuous.** `qrdb audit verify` and its
  Tier-1 equivalents on every transport run the check when
  something calls them; there is no startup check, no periodic scan,
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
  append-only chain; every archive's last record *is* pinned, by the anchor that terminates it.
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
as a standing guarantee between runs.

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
