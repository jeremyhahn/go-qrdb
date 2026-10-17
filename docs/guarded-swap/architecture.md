# Guarded Swap — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-22
> Conflicts with this document are defects. Fix them here first.

`sdk.GuardedSwap` is the optimistic compare-and-set loop over the transaction
plane: read a record, decide what it should become, and write that only if the
record has not moved underneath the decision.

## Problem

Three consumers hand-rolled the same loop — go-quicbaas's scheduler
firing-claim, its SDLC lease stores, and go-xkms's CA key store — and the two
quicbaas sites are the same primitive differing only in the refusal predicate.
Three independent implementations of one concurrency protocol is three
opportunities to get it subtly wrong, and the failure mode of getting it wrong
is a lost update that nothing reports.

The naive shape a consumer reaches for is read → decide → write. That is not a
compare-and-set: two callers read the same value, both decide, and both write,
and the second silently discards the first.

## Shape chosen

**A linearizable read, then one guarded transaction per attempt.**

```
GetRevision(key)                    <- value and ModRevision together
compares:   [guard on the record as read]
successOps: [Put(key, decided value)]
```

An attempt that **wins** costs one transaction: the guard and the write travel
together, so the whole swap is a read and a write rather than the hand-rolled
read, write and confirming re-read. An attempt whose guard is **refused** costs
one further read, because the record that beat it has to be read back before
the next decision.

### Why the read is not part of the transaction

The obvious optimisation — `failureOps: [Get(key)]`, so a refused guard reports
what beat it in the same round trip — is not available. **Transaction branches
carry writes only.** `pkg/service`'s `opTypeMap` maps `OpPut` and `OpDelete`,
and `encodeServiceOp` fails closed on anything else with
`UnsupportedTransactionOpError` before the command reaches Raft. A transaction
containing a read is rejected in full: no compare is evaluated and nothing is
applied.

That is a deliberate property of the transaction plane, not an oversight —
defaulting an unmapped op type to a write would let a caller's read request
overwrite the target key with an empty value. Reads therefore go through
`GetRevision`, the supported linearizable read, which returns the value and its
`ModRevision` together and so costs nothing the guard needs.

The first revision of this helper read through the transaction plane and passed
its entire unit suite, because the in-memory fake had implemented the reads
production refuses. `sdk/go/guardedswap_service_test.go` now exercises the loop
against the real `TransactionService` and `MVCCKVService`, and pins the refusal
directly.

## Decisions

### The swap guard is ModRevision, not the value bytes

A value guard admits the ABA case: a record that goes A → B → A between the
read and the swap satisfies a compare against A, so the write lands on top of a
state `Decide` never saw. That is safe only for records whose value moves
monotonically — a property of the **caller's** data that this package cannot
check.

A helper that is unsafe unless the caller happens to hold the right kind of data
is a trap, not a default. `GuardOnValue` remains for stores whose transaction
plane does not evaluate `CompareModRevision`.

### An absent record is guarded on CompareCreateRevision == 0

"This key has never been created" is a statement the state machine can evaluate
exactly. Comparing against an empty value cannot distinguish an absent key from
one holding no bytes.

### It fails closed, and distinguishes the two ways a loop fails to terminate

`SwapContendedError` — the attempt bound was reached and the record moved every
time. Genuine contention.

`SwapStalledError` — the guard was refused while the record did **not** move.
Reported on the first refusal rather than retried, because the next attempt
would build an identical guard against an identical record and be refused
identically. Spinning to the bound would report contention for a fault, and hide
that the store is not honouring the guard or does not evaluate the compare
target in use.

Neither is returned when a write landed, so a caller receiving either knows the
record is unchanged by that call.

### The decision belongs to the caller, the protocol does not

`Decide` returns write / done / lose. That is the whole extension point, and it
is what lets one implementation serve both consumer classes: a no-retry claim
returns `SwapLose` the moment it sees the record occupied, while a retrying
lease store returns `SwapWrite` until a higher generation is installed.

`Decide` must be a pure function of what it is shown. GuardedSwap may call it
several times, and calling it is not a commitment to write.

## Boundaries

Owned here: the loop, the guard construction, the terminal error vocabulary.

Not owned here: what a record means, whether it is monotonic, and how it is
encoded. Those are the caller's, and the helper deliberately takes and returns
raw bytes rather than inventing an encoding it would then have to version.

## Related

- `docs/distributed-locks/` and `docs/18-distributed-locking.md` — the lock
  primitives built on the same transaction plane.
- `kvstore.AtomicKVStore.CompareAndSwap` — compare-and-swap on the KV plane.
  It is a different surface, not an alternative to this one: it guards a store
  write to a TENANT-plane record, while this helper guards a transaction on the
  transport plane. Adding it to the store interface meant implementing it in
  every backend in the same change; see `docs/secondary-indexes/`.
