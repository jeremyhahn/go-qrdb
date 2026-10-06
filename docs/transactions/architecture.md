# Transactions and Batch — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Problem

Multi-key work needs two different guarantees, and conflating them produces a
primitive that is wrong for both. Some callers need *conditional* atomicity —
"write these keys only if that key still holds the value I read". Others need
*unconditional* atomicity — "apply these operations together, no predicate".

## Shape chosen

Two primitives, deliberately separate:

- **Transaction** — a compare-and-swap block: a list of `Compare` predicates,
  a `SuccessOps` list applied when every compare holds, and a `FailureOps` list
  applied when any does not. One Raft proposal, one revision.
- **Batch** — an unconditional list of `Op`s applied atomically at the KV layer.
  No predicates, no branch.

Both submit a single proposal to go-quicraft, so both are atomic with respect to
Raft commit ordering.

- **Import session** — a chunked upload lifecycle (`ImportBegin`/
  `ImportAppend`/`ImportCommit`/`ImportAbort`) for a dataset larger than one
  transport's pre-auth message cap (`transport.max_message_bytes`, 4 MiB
  default). Every chunk is an ordinary, small, authenticated
  unary call; the session's staged bytes are replicated, lease-bound state,
  not anything held in a node's memory. See "Import session" below.

### Alternatives rejected

- **Interactive transactions with an open server-side session** at this layer.
  Rejected: a held server-side transaction state is a liveness hazard across a
  Raft leadership change, and it is not needed for the KV use cases. Interactive
  transactions *do* exist, but at the SQL layer where the session already
  exists — see [../sql/](../sql/).
- **Batch as a transaction with zero compares.** Rejected in the API even though
  the mechanism is close: keeping them separate lets the batch path skip the
  predicate evaluation entirely and makes `batch:execute` a distinct permission
  from `transaction:execute`, which is the grant most callers actually want.
- **Client-side fan-out with retries.** Rejected: it cannot be atomic and it
  turns every consumer into a partial-failure handler.
- **A single-message `BatchImport` RPC carrying the whole file** (the
  earlier design). Rejected outright, no compat: it required a transport
  to accept a message far larger than anything else on its wire — 100 MB on
  REST/gRPC/Unix/MCP, ~63 MB on QUIC — which is exactly the allocate-before-
  auth exposure the universal pre-auth cap fixed everywhere else. There is no size at which
  "one big message" and "one small pre-auth cap, no exceptions" can both
  hold, so the capability moved to a session of many small calls instead.
- **An in-memory, per-connection session object** (the shape
  `pkg/rowlock.Session` uses for a SQL connection's lock leases). Rejected
  for import specifically: a SQL session is pinned to one TCP connection on
  one node for its whole life, so losing that connection ends the session
  anyway. An import session must survive exactly the thing a SQL session
  doesn't need to: the client's next chunk, or its commit, landing on a
  **different** node after a leader change or a reconnect through a load
  balancer. The session is therefore pure replicated state (see below), with
  no server process anywhere required to keep anything in memory for it to
  stay valid.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Transaction and batch semantics | `pkg/service`, `pkg/batch` | go-qrdb |
| Compare evaluation against MVCC state (`sm_type: mvcc`) | `pkg/mvcc`, `pkg/engine` | go-qrdb |
| Compare evaluation against a state machine's own keyspace (every other `sm_type`) | `pkg/store` (`pebble`), `pkg/statemachine` (`native_txn.go`) | go-qrdb |
| Import parsing (CSV/JSON/NDJSON/YAML/archive) | `pkg/service` (`BatchService.ImportCommit`), `pkg/codec` | go-qrdb, decoding via **go-codec** |
| Import session staging (reserved, lease-bound raw keyspace, every `sm_type`) | `pkg/importstage`, `pkg/service` (`BatchService.Import{Begin,Append,Commit,Abort}`), `pkg/engine` (`mvcc_import_stage.go`), `pkg/statemachine` (`kv_import_stage.go`) | go-qrdb |
| Session lease (TTL, cascade expiry on abandonment) | `pkg/lease` | go-qrdb |
| Single-proposal atomicity | — | **go-quicraft** |
| SDK surface | `sdk/go` (`Client.Transaction()`, `Client.Batch()`) | go-qrdb |

## Data flow

1. The caller builds a `TransactionRequest` (or an `[]Op`) and submits it on any
   transport.
2. Tenant and database are resolved server side; `transaction:execute` or
   `batch:execute` is checked.
3. For a transaction, every `Compare` is evaluated inside apply, against the
   state machine's current state: the revisioned MVCC state on `sm_type:
   mvcc`, the state machine's own keyspace — the one plain `Put` and `Get`
   use — on every other type.
4. The chosen op list is encoded into **one** Raft proposal.
5. On commit, all operations apply together and the response carries the
   resulting revision and per-op results.

A bulk import adds a staging stage in front of that: `ImportCommit` decodes
the session's combined staged bytes into records exactly as the old
single-message import decoded one file, and the records are applied —
either row by row (default) or as one all-or-nothing unit when
`--transactional` is set.

## Import session

A dataset larger than the pre-auth message cap is never sent as one
message on any transport. Instead the caller drives a four-call lifecycle,
every call an ordinary small, authenticated unary RPC:

1. **`ImportBegin(format, options)`** → `session_id`. Validates the options
   exactly as a single-message import did, grants a lease (the session's
   liveness) with a bounded TTL, and writes one hidden metadata record
   binding the session to the lease, to the caller's (tenant, identity),
   and to the (tenant, database) keyspace it will import into.
2. **`ImportAppend(session_id, seq, chunk)`** → ok. Stages `chunk` as a
   hidden record keyed by its sequence number and renews the session's
   lease, so an active upload never expires mid-stream. `seq` must be
   exactly one more than the last appended sequence (a new chunk) or equal
   to it with byte-identical content (an idempotent replay of the same
   call — a client that did not see the previous response retries safely);
   any other `seq` is refused.
3. **`ImportCommit(session_id)`** → `ImportResult`. Reads every staged
   chunk back in sequence order, concatenates them, and imports the result
   exactly as a single-message import always has — same parsing, same
   per-row error reporting, same `--transactional` all-or-nothing option.
   The staged chunks and the session's lease are then released.
4. **`ImportAbort(session_id)`** → ok. Releases the session's lease (which
   deletes every record bound to it — the metadata record and every staged
   chunk) without applying anything.

An abandoned session — the caller crashes or never calls `Commit`/`Abort`
— is reclaimed when its lease's TTL elapses: the SAME cascade-expiry
mechanism [../leases/](../leases/) already gives any lease-bound key,
nothing import-specific.

### Where the session lives

The session's metadata and staged chunks live under the reserved raw-
keyspace prefix `\x00qrdb.import/` (`pkg/importstage`), written only through
a dedicated Raft command (`protocol.OpImportStage`) — never through the
MVCC store or the KV engine's own `Put` path, on **every** `raft.sm_type`,
not just `mvcc`. This is the SAME treatment the lease table itself gets
(`pkg/lease.TablePrefix`), and it exists for the identical reason: a bulk
import has no narrower `sm_type` requirement than any other `BatchServicer`
capability, so `sm_type: pebble` (the default) must stage and commit an
import exactly as `sm_type: mvcc` does. They are:

- **Structurally unreachable from a client's `Get`/`Range`/`Put`/`List` of
  the same literal key** — not merely excluded from enumeration. `pkg/mvcc`'s
  own key encoding (`EncodeMetaKey`/`EncodeDataKey`) never produces the
  literal bytes under `\x00qrdb.import/`, and every state machine's client
  key validation refuses a client's own write under that prefix
  (`isStateMachineKey`), so a staged record is not a hidden key inside the
  ordinary keyspace — it is in a different keyspace altogether, reachable
  only through `protocol.OpImportStage`, which no client request encodes.
  An earlier design staged records as hidden MVCC keys, filtered only at
  enumeration, and a security review found that to be a discovery guard,
  not storage isolation — a plain `Get`/`Range`/`Put` of the literal key in
  an `IsolationRaft` database could read, overwrite, or forge it. This
  design closes that: see `pkg/engine/mvcc_import_stage.go` and
  `pkg/statemachine/kv_import_stage.go`.
- **Replicated, not node-local**: the write still commits through the same
  Raft proposal path as any other write, so it is included in snapshots —
  nothing new to teach the snapshot code, because the lease table's own
  raw keys already exercise this path.
- **Lease-bound**, every one of them, to the session's single lease ID
  (`pkg/lease.Lessor.Bind`), so revoking or expiring that lease deletes the
  metadata record, every staged chunk, and the tenant-index entry below
  through the SAME cascade an ordinary lease-bound key already gets, with
  no separate cleanup path to keep correct.
- **Counted per tenant, not per (tenant, database)**: `ImportBegin` writes
  a dedicated tenant-index entry (`pkg/importstage.TenantIndexKey`) in the
  SAME apply call as the metadata record, bound to the SAME lease, so the
  open-session quota is a property of the CALLING TENANT — independent of
  whether a particular session's own storage key carries a database
  namespace at all — and is counted by reading only that index's keys
  (`storage.IteratorOptions.OnlyKeys`), never a session's staged bytes.
- **Isolated per (tenant, database) the same way imported data already is**:
  the composite namespace prefix the per-database wrapper applies to every
  imported key (`ImportOptions.KeyPrefix`) is still recorded in the
  session's metadata at `Begin` and reused as part of the session's own
  storage key for every chunk — so two tenants (or two databases) sharing
  one underlying shard under prefix isolation can never collide on a
  session ID, even an adversarially-chosen one.

Because every record is lease-bound, staging needs an engine that keeps
leases. The plain KV engine a standalone node composes for the `file` and
`objstore` backends keeps none, so it refuses a staging command as an
unsupported capability and writes nothing (`pkg/engine/kv_import_stage.go`),
the same answer the MVCC engine and the `pebble` state machine give when
built without a lessor. A record staged without a lease could never be
reclaimed.

### Why not the ordinary MVCC keyspace

An earlier design kept a session's metadata and chunks as hidden, user-
shaped MVCC keys, reusing the EXISTING internal-key-prefix convention
(`pkg/service.IsInternalKey`) that already hides `__sys:`/`__tbl:`/`__idx:`/
`ts:` records from bulk enumeration. That required `sm_type: mvcc` — the
only state machine with a keyspace those hidden keys could live in — and,
more seriously, was reachable by any client write of the identical literal
key in a database whose isolation does not filter by key shape
(`IsolationRaft`). The lease table's own raw, non-MVCC, every-`sm_type`
storage design (`pkg/lease.TablePrefix`) was rejected as "too invasive" when
this feature first shipped; a later security review overturned that
call: a session with no narrower `sm_type` requirement than any other
`BatchServicer` capability has no justification for a keyspace only one
`sm_type` provides, and "ordinary MVCC-managed key, hidden at enumeration"
is a guard a client with direct Raft access through `IsolationRaft` can
step around entirely. `pkg/importstage` now mirrors the lease table's own
raw-keyspace, lease-bound, every-`sm_type` design exactly, rather than
inventing a second variant of it.

### Transactions on every state machine

Every `raft.sm_type` executes transactions and batches. Only `mvcc` keeps
revisions; every other type — `pebble` (the default), `memory`, `standard`,
`concurrent`, `fast`, `disk`, `highperf`, `regular` — decides a transaction
against its own keyspace with the same compare semantics:

- **Value** compares against the stored value; an absent key compares as the
  empty value.
- **Version** is 0 when the key is absent and 1 when it is present — exactly
  enough for "must be absent" (`version == 0`), the fence every guarded create
  is built on.
- **Create- and mod-revision compares are refused**, with the error
  `transaction: create_revision/mod_revision compares require an MVCC state
  machine`, before anything is evaluated. Answering "revision 0" for every
  key would let two read-then-CAS writers both win and lose an update with no
  signal.
- **A write that carries a lease binds its key to that lease**, on every
  `pkg/statemachine` type except the legacy `regular`, which keeps no lease
  table and refuses the whole set with a capability error. A lease ID the
  state machine's own table does not hold refuses the whole set with the same
  `lease-not-found` error a plain lease-bound `Put` naming it would give — not
  a capability refusal, since the state machine can bind leases and applying
  the write without one would make a key meant to expire live forever.
- **An empty key is refused**, by the service layer before it is proposed and
  by every state machine at apply.

A separate MVCC keyspace beside those state machines was the rejected
alternative: a compare there cannot see a plain `Put`, so a guarded batch
fenced on a claim would fence on nothing.

## Trust boundaries

- Transactions and batches are scoped to a single (tenant, database) pair, like
  every other data-plane operation.
- A branch only writes: `OpGet` and `OpRange` are refused before anything is
  proposed (`UnsupportedTransactionOpError`), so a transaction returns no
  key data and the internal-key read guard has nothing to filter. An unmapped
  op type fails closed rather than defaulting to a write.
- **Every import call — `ImportBegin`/`ImportAppend`/`ImportCommit`/
  `ImportAbort`, like every other RPC — is bounded by the universal
  pre-auth message cap** (`transport.max_message_bytes`, default 4 MiB),
  identical on every transport and checked before authentication runs.
  There is no opt-in to a larger single message; a dataset bigger than one
  chunk is spread across more `ImportAppend` calls, not a bigger one.
- **A session is confined to the caller that opened it.** `ImportAppend`/
  `ImportCommit`/`ImportAbort` re-authenticate the SAME (tenant, identity)
  `ImportBegin` recorded, checked explicitly against the request's
  authenticated identity — never inferred from the session ID being hard
  to guess. A session ID is not a credential.
- **A session is also confined to the (tenant, DATABASE) namespace it was
  begun through.** The per-database namespaced wrapper (`pkg/server`)
  refuses `ImportAppend`/`ImportCommit`/`ImportAbort` for a session minted
  through a DIFFERENT database of the same tenant with the SAME
  identity — `checkSessionOwner`'s (tenant, identity) check alone cannot
  catch this, since both match. Refused as `*ImportSessionNotFoundError`,
  identical to a session that does not exist, rather than a distinct error
  that would confirm the session exists in some other database.
- **Quotas are enforced after authentication**: a per-session total byte
  budget, a per-session chunk-count ceiling, and a per-tenant concurrent-
  session ceiling, all configurable. They exist so an authenticated but
  misbehaving caller cannot stage an unbounded amount of data or open an
  unbounded number of sessions while waiting for its lease to expire.

## What this feature deliberately does not do

- **No interactive, multi-round-trip KV transaction.** That is a SQL-layer
  capability.
- **No cross-database or cross-tenant atomicity.** A single request never spans
  two scopes.
- **No collection-targeted import.** Imports always land in the raw KV keyspace;
  documents go in one at a time through `qrdb collection doc insert`.
- **No automatic retry on a failed compare.** A failed compare is a result, not
  an error, and the caller decides what to do about it.
- **No multi-proposal `--transactional` commit.** All-or-nothing atomicity
  comes from Raft's single-proposal guarantee (see "Single-proposal
  atomicity" above), and there is no such guarantee across more than one
  proposal. A `--transactional` `ImportCommit` whose combined staged bytes
  exceed `maxImportProposalBytes` (8 MiB) is refused outright
  (`*ImportProposalTooLargeError`), before anything is proposed, rather than
  attempted non-atomically or silently split into several proposals. This
  is the accepted design boundary, not a defect to lift: a caller whose
  dataset exceeds it either imports without `--transactional` — the
  combined staged bytes still commit, row by row, with no size ceiling of
  their own beyond `import_session.max_session_bytes` — or splits the
  import into multiple transactional sessions, each under the ceiling, and
  retries `ImportCommit` on whichever one fails (the session and its staged
  chunks survive a failed commit for exactly this reason — see "Import
  session" and the error-semantics table in
  [engineering.md](engineering.md)).
