# A storage outage is indistinguishable from an unknown invitation token

**Status:** open design gap, found 2026-08-15 while raising `pkg/invite`
coverage. Needs an owner decision; **not** fixable inside `pkg/invite`.

## Symptom

`invite.DAOStore.Lookup` and `.Redeem` return `ErrNotFound` when the backing
store is unavailable, exactly as they do for a token that was never issued.

A user presenting a **valid** invitation during a storage blip is told the
invitation does not exist, and an operator reading the logs sees the same.

## Mechanism

`DAOStore.find` collapses every error from the DAO into `ErrNotFound`. That
looks like a sloppy `if err != nil` — but tightening it in place does not work,
because the information is already gone by the time `find` sees the error.

`kvstore.KVStore.Get` is specified as returning **`(nil, nil)` for a missing
key** (`pkg/kvstore/kvstore.go:78`) rather than a sentinel error. So:

- a **miss** reaches the DAO as empty bytes, and surfaces as a deserialization
  failure (`dao: failed to deserialize invites: unexpected end of JSON input`);
- a **read fault** surfaces as some other error.

Both are just `error` at the `find` call site, and the miss does **not** arrive
as a `dao.NotFoundError`. An `errors.As(err, &dao.NotFoundError{})` check
therefore classifies every ordinary miss as a storage failure — this was tried
on 2026-08-15 and broke three existing tests, which is what exposed the real
mechanism.

## Why it is not a `pkg/invite` fix

Separating the two cases requires a not-found signal that survives the
KV → DAO → store journey. The options all live below this package:

1. **Give `KVStore.Get` a not-found error.** The honest fix, and a breaking
   change to a contract with many implementations and callers.
2. **Give the DAO layer an `Exists`-then-`Get` path** (or have it treat empty
   bytes as a typed `dao.NotFoundError`), so a miss is distinguishable by the
   time any consumer sees it. Contained to `pkg/dao`, and would fix this for
   every DAO consumer rather than only invitations.
3. **Leave it**, and accept that invitation lookups cannot distinguish an
   outage from an unknown token.

Option 2 looks cheapest and highest-value, but it changes shared behaviour for
every DAO consumer, so it is an owner decision rather than a local cleanup.

## Blast radius

Anything reading through the generic DAO over a `KVStore` has the same
ambiguity; invitations are simply where it is most visible, because the
user-facing consequence ("your invitation is invalid") is indistinguishable
from the security-relevant one ("this token was forged").

## Current state

`find` keeps the collapsing behaviour with a comment recording why, and
`pkg/invite`'s storage-failure tests assert only that the operation fails —
deliberately not that the cause is recoverable from the error. Tightening
those assertions is the signal that this gap has been closed.
