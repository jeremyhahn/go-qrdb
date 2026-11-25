# Main fix #1722: keep the last QUIC Connect refusal when the bound cuts the final attempt

## Problem

`Transport.Connect` in `sdk/go/transport/quic` retries until the caller's
bound (context deadline or cancel) is spent and then reports the last
attempt's failure. When the bound cut the final attempt short, that attempt's
error (a generic timeout or cancel) replaced the server refusal held from the
attempt before it. The caller saw a timeout instead of the real reason the
server refused the connection.

## Fix

`lastConnectFailure(last, err, spent)` decides which failure Connect reports.
Once a failure is held, it keeps it when the newest attempt's error wraps
`context.DeadlineExceeded` or `context.Canceled`, or when the bound is
already spent (`boundSpent`: ctx is done, or its deadline has passed before
its timer fired). Otherwise the newest error replaces it. Before the first
failure, the newest error is taken as is.

## Test

`TestLastConnectFailure_KeepsRefusalOverBoundCut`
(`sdk/go/transport/quic/connect_last_test.go`) covers keeping the held refusal
over a bound-cut attempt and taking a real newer failure.

## Done

- [x] Fix and regression test.
- [x] Main merged into the branch (CI workflow and testdata changes only).
- [x] `boundSpent`'s doc comment moved back above `func boundSpent`; it had
  been split from its func by the `lastConnectFailure` insertion.
