# Guarded Swap — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-26 at main 851fd06fc3
> Conflicts with this document are defects. Fix them here first.

**There is no CLI surface for guarded swap, and there is deliberately no plan
to add one.**

The operation is defined by its `Decide` callback — a program's decision about
what a record should become, given what it currently is. A command line can
carry a key and two byte strings; it cannot carry a decision function. So a
`qrdb swap` verb could only ever express the degenerate case, "write this if
the record is currently exactly that", and even that would be one attempt
rather than the loop.

What the CLI cannot offer is the part that matters: re-reading after a refusal,
re-deciding on the new value, the progress precondition that separates a stall
from contention, and the bounded retry. Those need code, which is why this
feature ships as an SDK helper.

The transaction plane it composes does have a verb, `qrdb tx execute`
([../transactions/cli.md](../transactions/cli.md)), which submits one
caller-written compare-and-swap transaction from a JSON file. That is a single
attempt with the guard spelled out by hand, not a guarded swap. The locked CLI
surface is defined in the handbook's `docs/11-cli-surface.md`, not in this
repository; new verbs are added there, not per-feature.

Integration coverage: the feature-gate activation of guarded writes is driven
over every remote transport by `make integration-test-guarded-swap`
(`feature_gate_cluster_test.go`). No integration test drives
`sdk.GuardedSwap` itself; see the `guarded-swap` row of
[`test/integration/COVERAGE.md`](../../test/integration/COVERAGE.md).
