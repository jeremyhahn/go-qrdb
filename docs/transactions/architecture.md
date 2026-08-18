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
| Compare evaluation against MVCC state | `pkg/mvcc` | go-qrdb |
| Import parsing (CSV/JSON/NDJSON/YAML/archive) | `pkg/service` (batch import), `pkg/codec` | go-qrdb, decoding via **go-codec** |
| Single-proposal atomicity | — | **go-quicraft** |
| SDK surface | `sdk/go` (`Client.Transaction()`, `Client.Batch()`) | go-qrdb |

## Data flow

1. The caller builds a `TransactionRequest` (or an `[]Op`) and submits it on any
   transport.
2. Tenant and database are resolved server side; `transaction:execute` or
   `batch:execute` is checked.
3. For a transaction, every `Compare` is evaluated against the current MVCC
   state under the same snapshot.
4. The chosen op list is encoded into **one** Raft proposal.
5. On commit, all operations apply together and the response carries the
   resulting revision and per-op results.

Batch import adds a parsing stage in front: the file is decoded into records,
each record yields a key and a value, and the records are applied — either row
by row (default) or as one all-or-nothing unit when `--transactional` is set.

## Trust boundaries

- Transactions and batches are scoped to a single (tenant, database) pair, like
  every other data-plane operation.
- The internal-key read guard applies to any `OpRange`/`OpGet` result the
  request produces.
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
