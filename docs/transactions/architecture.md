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

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Transaction and batch semantics | `pkg/service`, `pkg/batch` | go-qrdb |
| Compare evaluation against MVCC state (`sm_type: mvcc`) | `pkg/mvcc`, `pkg/engine` | go-qrdb |
| Compare evaluation against a state machine's own keyspace (every other `sm_type`) | `pkg/store` (`pebble`), `pkg/statemachine` (`native_txn.go`) | go-qrdb |
| Import parsing (CSV/JSON/NDJSON/YAML/archive) | `pkg/service` (batch import), `pkg/codec` | go-qrdb, decoding via **go-codec** |
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

Batch import adds a parsing stage in front: the file is decoded into records,
each record yields a key and a value, and the records are applied — either row
by row (default) or as one all-or-nothing unit when `--transactional` is set.

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
- **Import size is bounded per transport**, which is a denial-of-service control
  as much as a wire limit: REST, gRPC, Unix and MCP accept up to 100 MB; QUIC
  accepts roughly 63 MB, bounded by the QUIC wire protocol's shared per-RPC
  payload cap rather than an import-specific rule.

## What this feature deliberately does not do

- **No interactive, multi-round-trip KV transaction.** That is a SQL-layer
  capability.
- **No cross-database or cross-tenant atomicity.** A single request never spans
  two scopes.
- **No collection-targeted import.** Imports always land in the raw KV keyspace;
  documents go in one at a time through `qrdb collection doc insert`.
- **No automatic retry on a failed compare.** A failed compare is a result, not
  an error, and the caller decides what to do about it.
