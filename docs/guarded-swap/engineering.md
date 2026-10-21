# Guarded Swap — Engineering

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

## Where it lives

| file | holds |
|---|---|
| `sdk/go/guardedswap.go` | the loop, guard construction, the `SwapPlane` contract |
| `sdk/go/guardedswap_errors.go` | the four terminal error types |
| `sdk/go/guardedswap_plane_test.go` | an in-memory transaction plane that evaluates compares atomically |
| `sdk/go/guardedswap_test.go` | the behavioural and race tests |
| `sdk/go/guardedswap_service_test.go` | the same loop against the REAL transaction and MVCC services |

## The loop

1. Read the record with `GetRevision`, which returns the value and its
   `ModRevision` together. A missing key (`*transport.KeyNotFoundError`, or a
   nil record) is absence, not failure.
2. Call `Decide`. Stop on `SwapDone` / `SwapLose` / an error.
3. Build the guard — `CompareCreateRevision == 0` when absent, otherwise
   `CompareModRevision == readRevision` (or `CompareValue` under
   `GuardOnValue`).
4. Execute `{guard, OpPut(write)}` with **no failure ops**. Success returns.
5. On refusal, re-read with step 1's call and go to 2.

Step 4 carries no failure branch because transaction branches carry writes
only; see [architecture.md](architecture.md) for the constraint and why the
read cannot be folded in. Steps 1 and 5 deliberately share one code path
(`swapRead`), so the initial read and the read-back resolve absence
identically — otherwise the progress precondition below would be comparing two
different things.

## The progress precondition

Between step 5 and step 2 the loop checks that the record actually **moved**. If
the guard was refused and the record is byte-identical with the same revision
and the same existence, retrying cannot change the outcome — the next attempt
builds the identical guard against the identical record. That is
`SwapStalledError`, reported immediately.

Without it the loop spins to `MaxAttempts` and reports *contention*, which sends
whoever is debugging it to look for competing writers that do not exist.

## Testing notes — read before adding tests here

**A race test over an in-process fake is vacuous unless the participants are
held at a barrier.** An in-memory plane is fast enough that each writer
finishes its whole read-decide-write cycle before the next one starts, so no
two are ever in flight and "exactly one winner" holds whether or not a guard
exists: without a barrier the test passes with the guard **removed entirely**.

`fakeTxnPlane.gateReads(n)` holds every initial read until all `n` have arrived,
so every writer decides against the same pre-write record and only the guard can
separate them. With the gate, deleting the guard fails the test.

Generalise it: any "exactly one winner" assertion over a fast in-process fake
needs a barrier that holds every participant at the read, or it is asserting the
scheduler's behaviour rather than the code's.

**Drive the stall and bound paths deterministically**, not by timing:
`refusingPlane` refuses every guard and serves whatever the test chooses —
an unchanging record for the stall, a changing one for contention — and the
tests assert the round-trip COUNT: a stall under `MaxAttempts: 25` costs one
transaction and two reads, not 25 transactions.

## Mutation checks that must keep failing

| mutation | test that must fail |
|---|---|
| remove the guard from the transaction | `TestGuardedSwap_NoRetryPredicateAdmitsExactlyOneWinner`, `TestGuardedSwap_ConcurrentCreatorsAdmitExactlyOneCreate` |
| remove the progress precondition | `TestGuardedSwap_RefusedWithoutMovementIsAStall` |
| default a negative `MaxAttempts` | `TestGuardedSwap_RejectsAnUnusableRequest/negative_MaxAttempts` |
| drop `Revision` in a transport | `TestEmbeddedTx_PropagatesTheRevision` |

## Integration testing

`make integration-test-guarded-swap` runs this feature's self-spawned integration tests: `feature_gate_cluster_test.go`. The per-transport accounting, including every gap, is the `guarded-swap` row of [test/integration/COVERAGE.md](../../test/integration/COVERAGE.md).
