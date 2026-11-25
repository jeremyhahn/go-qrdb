# #566 sleep sweep 9 (rw566n)

Sweep 9 takes the `time.Sleep` polls left in `test/integration` (the ones sweep
8 rows 46-48 left to a later sweep) and in the `test/performance` harnesses
(`leasekeepalive` and the `test/performance/qrdb` module). Line numbers below
are the lines before the sweep. In `test/integration` a deadline loop becomes
`ok := pollUntil(<same timeout>, <same interval>, func() bool { ... })`, the
house helper in `helpers_test.go`: it evaluates the condition at once and then
on each tick, synchronously on the test goroutine, so the condition records the
last observation (and may call `t.Fatalf`), and the failure check and text after
the loop stay as they were. The performance harnesses have no `*testing.T` to
hand to a helper (`leasekeepalive` is a `package main` returning errors, the
bench is a `*testing.B` that calls `b.Fatalf` after the loop), so, as in sweeps
2 and 8, the sleep becomes a ticker wait (`poll := time.NewTicker(d); defer
poll.Stop()` before the loop, `<-poll.C` in the sleep's place) with the same
deadline and error text. Either way the attempts are paced by a channel the
test owns instead of a sleep standing in for synchronisation (ruling 273(12)),
and no behaviour loses its test: each rewritten wait is exercised by the tests
that call it.

| File | Line | Replaced by / kept and why |
|------|------|----------------------------|
| test/integration/helpers_test.go | 3025 | `waitForNodeToTrustClient`: `pollUntil` (timeout, 250ms); the condition records `lastErr`, drains and closes the body on success |
| test/integration/isolation_raft_cluster_test.go | 610 | `assertPerNodeTenantKeys`: `pollUntil` (timeout, 250ms); the condition records body, code and status (status is not reset between attempts); the after-loop and duplicate-fingerprint checks are unchanged |
| test/integration/isolation_raft_cluster_test.go | 654 | `assertTenantUsersOnEveryNode` lookup loop: `pollUntil` (timeout, 250ms) until code is 200; body and code recorded |
| test/integration/tenant_undo_reuse_cli_test.go | 160 | `awaitReleased` in `TestTenantUndo_IDReuseAwaitsEveryNode_PerProtocol`: `pollUntil` (90s, 1s); the condition fails at once on output without "was undone and", as before; on timeout `id still pending` with the last output |
| test/performance/leasekeepalive/main.go | 287 | `waitHealthy`: ticker wait (100ms), same deadline and error |
| test/performance/leasekeepalive/main.go | 323 | `waitLeader`: ticker wait (100ms), same deadline and error |
| test/performance/qrdb/bench.go | 529 | `waitForAdminHealthy`: ticker wait (50ms), same `b.Fatalf` |
| test/performance/qrdb/bench.go | 552 | `waitForLeader`: ticker wait (100ms), same `b.Fatalf` |
| test/performance/qrdb/bench.go | 580 | `warmupWrite`: ticker wait (200ms), same `b.Fatalf` |
| pkg/api/etcd/watch_compacted_test.go | 168 | Kept: `overlapDetectingStream.Send` holds the send open 50us so two overlapping Sends are observable; it widens the window under test, it is not a wait |
| pkg/clock/clock.go | 88 | Kept: the real clock's `Sleep`, production code behind the injectable clock |
| pkg/server/tenant_host.go | 361 | Kept: the real shard-leader clock's `Sleep`, production code behind the injectable clock |
| cmd/qrdbd/signal_guard_test.go | 160 | Kept: comment only |
| pkg/clock/clock.go | 23 | Kept: comment only |
| pkg/credentials/versioned_test.go | 47, 63 | Kept: comments only |
| pkg/lock/locker_test.go | 451 | Kept: comment only |
| pkg/server/sealed_listener.go | 105 | Kept: comment only |
| pkg/watch/manager_test.go | 21 | Kept: comment only |

## Remaining time.Sleep inventory

Every hit of `git grep -n 'time\.Sleep' -- '*.go'` after this sweep, excluding
the sweep 7 packages (`pkg/api/connlimit`, `pkg/api/grpc`, `pkg/api/quic`,
`pkg/sql/pgsql`, `pkg/statemachine`, `pkg/firstboot`, `pkg/rowlock`: lane
rw566j records those). Lines are after the sweep. "Audit" is
`docs/plans/566-suite-audit.md` (sweeps 2-6), cited by its line.

| File | Line | Recorded as kept in |
|------|------|---------------------|
| cmd/qrdbd/coverage_target_test.go | 150 | Audit:512 |
| cmd/qrdbd/fullstart_integration_test.go | 187 | Audit:513 |
| cmd/qrdbd/noise_port_unix_test.go | 51 | Audit:516 |
| cmd/qrdbd/reset_signal_race_test.go | 108 | Audit:519 |
| cmd/qrdbd/signal_dispatch_test.go | 95 | Audit:520 |
| cmd/qrdbd/signal_guard_test.go | 160 | This file (comment only) |
| pkg/api/etcd/watch_compacted_test.go | 168 | This file |
| pkg/backend/group_commit_test.go | 70 | Audit:573 |
| pkg/clock/clock.go | 23, 88 | This file |
| pkg/credentials/versioned_test.go | 47, 63 | This file (comments only) |
| pkg/lock/locker_test.go | 451 | This file (comment only) |
| pkg/server/prebound_quic_test.go | 112 | Audit:474 |
| pkg/server/sealed_listener.go | 105 | This file (comment only) |
| pkg/server/system_shard_settling_test.go | 196, 198, 248 | Audit:486 |
| pkg/server/tenant_host.go | 361 | This file |
| pkg/service/invitation_test.go | 461 | Audit:536 |
| pkg/service/reclaim_job_test.go | 786 | Audit:537 |
| pkg/sql/internal/sessionguard/sessionguard_test.go | 180, 206 | Audit:572 |
| pkg/sql/mysql/idle_timeout_wire_test.go | 108 | Audit:488 |
| pkg/trustanchor/kv_atomic_test.go | 43 | Audit:574 |
| pkg/volume/reporter_seal_test.go | 169, 176, 203 | Audit:450, 539, 540 |
| pkg/watch/manager_test.go | 21 | This file (comment only) |
| sdk/go/coordination_raft_test.go | 832 | docs/plans/566-sweep-8.md:35 |
| sdk/go/coordination_test.go | 1036 | Audit:457; docs/plans/566-sweep-8.md:36 |
| sdk/go/gorm/conformance/qrdb_harness_test.go | 539 | docs/plans/566-sweep-8.md:40 |
| sdk/go/testutil/qrdbdharness/daemon_test.go | 59, 65 | docs/plans/566-sweep-8.md:38 |
| sdk/go/testutil/qrdbdharness/startready_test.go | 66 | docs/plans/566-sweep-8.md:39 |
| sdk/go/transport/coalescing_test.go | 262, 274 | Audit:454; docs/plans/566-sweep-8.md:37 |
| sdk/go/types_request_error.go | 26 | docs/plans/566-sweep-8.md:41 (doc comment only) |
| test/clusterproof/soak/soak_test.go | 58, 69, 80, 91 | docs/plans/566-sweep-8.md:42 |
| test/integration/cluster_seal_test.go | 449 | docs/plans/566-sweep-8.md:43 |
| test/integration/watch_cli_test.go | 394, 833 | docs/plans/566-sweep-8.md:44, 45 |

## CHANGELOG line (ready to paste)

- Tests: test/integration waits on pollUntil and the test/performance harnesses on a ticker instead of time.Sleep between attempts (#566)
