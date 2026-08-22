# Guarded Swap — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-22

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

Note that `qrdb` exposes no transaction verb at all today — there is no
`qrdb txn` to build on, and adding one is a change to the locked CLI surface,
not a guarded-swap decision. The locked CLI surface is defined in the Software
Department's `docs/11-cli-surface.md`, not in this repository; new verbs are
added there, not per-feature.

Operators looking for contention should read the transaction plane's existing
metrics rather than expect a swap-specific verb: a swap that loses its guard is
an ordinary refused transaction from the store's point of view, and is already
counted as one.
